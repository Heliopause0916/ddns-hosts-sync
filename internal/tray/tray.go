package tray

import (
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/state"
)

// pollInterval 状态轮询间隔（DSD §4.5：GUI 每 10s 轮询 status.json）。
const pollInterval = 10 * time.Second

// Deps Tray 的全部外部依赖，路径与动作由调用方（cmd，协调者）注入：
//   - 本包不 import internal/platform，LockPath/StatusPath 等由上层解析；
//   - 窗口打开/退出动作由协调者经回调接入 fyne 主 goroutine。
type Deps struct {
	LockPath    string // 单实例锁文件路径（建议位于 Users 可写目录，如 config 同级）
	ConfigPath  string // config.yaml 路径（托盘"暂停同步"直读写）
	StatusPath  string // state/status.json 路径（托盘读）
	TriggerPath string // state/trigger.json 路径（托盘"立即同步"写）

	OpenConfig  func() // 打开配置窗口（协调者负责 fyne.Do 入主循环 + 单实例窗口）
	OpenLogsDir func() // 打开日志目录（资源管理器定位）
	TriggerTask func() // 尽力触发计划任务（协调者接 platform.TriggerTask，失败静默）
	OnQuit      func() // 退出钩子（协调者提示"仅关闭本窗口，后台自动同步不受影响"）
}

// traySvc 抽象 fyne.io/systray 的命令面：headless 无法启动真实系统托盘，
// 单测注入 fake 断言刷新逻辑；生产实现见 systray_host.go（薄层）。
type traySvc interface {
	Run(onReady, onExit func())
	Quit()
	ResetMenu()
	AddItem(title, tooltip string, checkable, checked bool) MenuHandle
	AddSeparator()
	SetIcon(png []byte)
	SetTitle(title string)
	SetTooltip(text string)
	SetOnTapped(f func())
}

// MenuHandle 菜单项的运行时句柄（点击订阅 + 勾选/禁用态更新）。
type MenuHandle interface {
	Clicked() <-chan struct{}
	Check(checked bool)
}

// Tray 托盘生命周期：单实例锁 → 系统托盘循环 → 状态轮询 → 事件串行执行。
//
// 线程模型：所有 systray 命令面调用与业务回调（菜单点击、轮询刷新、退出）
// 经 queue 在单一 goroutine 串行执行，杜绝与 systray 事件线程并发（DSD §7.1
// "tray callbacks 须入主 goroutine"——协调者将窗口回调再经 fyne.Do 提入 fyne
// 主循环，此处保证 systray 侧无并发竞争）。
type Tray struct {
	deps Deps
	svc  traySvc

	queue    chan func()
	done     chan struct{}
	quitOnce sync.Once

	color  Color
	paused bool
	items  map[string]MenuHandle
}

// New 构造 Tray（默认绑定 fyne.io/systray 命令面）。
func New(deps Deps) *Tray {
	return &Tray{
		deps:  deps,
		svc:   newSystrayHost(),
		queue: make(chan func(), 64),
		done:  make(chan struct{}),
		items: make(map[string]MenuHandle),
	}
}

// Run 启动托盘（阻塞直至退出）。返回 ErrAlreadyRunning 时调用方提示"程序已在
// 运行"并正常退出（DSD §3.4）。
func (t *Tray) Run() error {
	lock, err := AcquireInstanceLock(t.deps.LockPath, InstanceLockStaleAge)
	if err != nil {
		return err
	}
	defer lock.Release()

	t.svc.Run(t.onReady, t.onExit)
	<-t.done
	return nil
}

// onReady 系统托盘就绪：置灰图标与启动文案 → 构建菜单 → 启动事件循环。
func (t *Tray) onReady() {
	icon, err := trayIcon(ColorGray)
	if err == nil {
		t.svc.SetIcon(icon)
	}
	t.svc.SetTitle("ddns-hosts-sync")
	t.svc.SetTooltip("后台同步未运行，请检查计划任务")
	t.rebuildMenu(t.paused)
	t.svc.SetOnTapped(func() { t.post(t.deps.OpenConfig) })

	// 启动串行事件消费 goroutine 与状态轮询。
	go t.runLoop()
	go t.pollLoop()
}

// onExit 系统托盘循环结束：广播 Run 返回。
func (t *Tray) onExit() {
	close(t.done)
}

// runLoop 串行消费事件队列（菜单点击/轮询刷新/退出等）。
func (t *Tray) runLoop() {
	for fn := range t.queue {
		if fn != nil {
			fn()
		}
	}
}

// pollLoop 每 pollInterval 投递一次状态刷新（systray API 串行执行）。
func (t *Tray) pollLoop() {
	tick := time.NewTicker(pollInterval)
	defer tick.Stop()
	for {
		select {
		case <-tick.C:
			t.post(t.refresh)
		case <-t.done:
			return
		}
	}
}

// post 向队列投递事件（非阻塞；system tray 已退出时静默丢弃）。
func (t *Tray) post(fn func()) {
	if fn == nil {
		return
	}
	select {
	case t.queue <- fn:
	default:
	}
}

// Quit 请求托盘退出（内部，菜单/协调者经事件调用）。
func (t *Tray) Quit() {
	t.quitOnce.Do(func() {
		t.post(func() {
			t.svc.Quit()
			t.deps.OnQuit() // 协调者提示 + fyne 收尾
		})
	})
}

// rebuildMenu 依据当前暂停态重建右键菜单并绑定点击事件。
func (t *Tray) rebuildMenu(paused bool) {
	t.svc.ResetMenu()
	t.items = make(map[string]MenuHandle)
	for _, spec := range BuildMenuSpec(paused) {
		if spec.Separator {
			t.svc.AddSeparator()
			continue
		}
		h := t.svc.AddItem(spec.Label, "", spec.Checkable, spec.Checked)
		t.items[spec.ID] = h
		go t.watchClick(spec.ID, h)
	}
}

// watchClick 订阅菜单项点击并投递对应业务动作。
func (t *Tray) watchClick(id string, h MenuHandle) {
	for range h.Clicked() {
		t.dispatch(id)
	}
}

// dispatch 按菜单 ID 分发业务动作（在串行事件 goroutine 中执行）。
func (t *Tray) dispatch(id string) {
	switch id {
	case MenuOpenConfig:
		t.deps.OpenConfig()
	case MenuSyncNow:
		t.syncNow()
	case MenuViewLogs:
		t.deps.OpenLogsDir()
	case MenuPauseToggle:
		t.togglePause()
	case MenuQuit:
		t.Quit()
	}
}

// syncNow 立即同步：写 trigger + 尽力触发任务（DSD §4.5 时序步骤 1-3）。
// /run 失败静默（step 3 注释：普通用户常被拒，兜底由任务侧新鲜度保证）。
// 防御性创建 trigger 所在目录：托盘可能先于 install 启动（此时 state/ 尚未建立）。
func (t *Tray) syncNow() {
	tg, ok := newSyncNowTrigger(time.Now().UTC())
	if !ok {
		return
	}
	if dir := filepath.Dir(t.deps.TriggerPath); dir != "." && dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return
		}
	}
	_ = state.WriteTrigger(t.deps.TriggerPath, tg)
	if t.deps.TriggerTask != nil {
		t.deps.TriggerTask()
	}
	t.refresh() // 立即刷新一次状态（图标/文案不立刻变，但保持 cache 同步）
}

// togglePause 翻转"暂停自动同步"并写 config（自动保存语义，DSD §4.1）。
func (t *Tray) togglePause() {
	cfg, err := config.Load(t.deps.ConfigPath)
	if err != nil {
		return
	}
	cfg.Enabled = !cfg.Enabled
	if err := cfg.Save(t.deps.ConfigPath); err != nil {
		return
	}
	t.paused = !cfg.Enabled
	t.syncPauseItem()
	if !t.paused {
		// 恢复同步：顺带请求一轮立即同步（真实场景由 token 兜底，这里尽力）。
		t.syncNow()
	}
}

// syncPauseItem 同步菜单勾选态（无菜单重建，避免 Windows 托盘闪烁）。
func (t *Tray) syncPauseItem() {
	if h, ok := t.items[MenuPauseToggle]; ok {
		h.Check(t.paused)
	}
}

// refresh 读取 status.json 并应用四色图标/tooltip（事件队列内调用）。
// 读取失败（瞬时 rename 窗口）沿用上次状态，不改变图标。
func (t *Tray) refresh() {
	s, _, err := state.ReadStatus(t.deps.StatusPath)
	if err != nil && s == nil {
		return
	}
	color := DecideColor(s, time.Now())
	icon, ierr := trayIcon(color)
	if color != t.color && ierr == nil {
		t.color = color
		t.svc.SetIcon(icon)
		t.svc.SetTooltip(Tooltip(s, color))
	}
}

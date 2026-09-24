// Package gui 实现 fyne 配置窗口（DSD §4.1 组件树），与托盘解耦：
// 独立于 tray 构造，可被 cmd 或测试单独实例化（真实 app 或 fyne test 驱动）。
//
// 导入方向硬约束：本包不 import internal/platform、internal/hostsfile、
// internal/resolver；路径与系统动作经 Deps 注入；read config/status、写
// trigger 复用 internal/config 与 internal/state（DSD §2.4、判定逻辑单源）。
package gui

import (
	"sync"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// WindowTitle 配置窗口标题（DSD §4.1）。
const WindowTitle = "ddns-hosts-sync 配置"

// statusPollInterval 状态轮询间隔（DSD §4.1：每 10s）。
const statusPollInterval = 10 * time.Second

// Deps 配置窗口外部依赖（由 cmd 协调者注入；测试注入临时路径与 fake 触发）。
type Deps struct {
	App         fyne.App
	ConfigPath  string
	StateDir    string // status.json / trigger.json 所在目录
	LogPath     string
	TriggerNow  func() // 尽力触发计划任务（协调者接 platform.TriggerTask；失败静默）
	OpenLogsDir func() // 打开日志目录（资源管理器定位；协调者注入）
}

// statusPath / triggerPath 便捷拼接。
func (d Deps) statusPath() string  { return d.StateDir + "/status.json" }
func (d Deps) triggerPath() string { return d.StateDir + "/trigger.json" }

// GUI 配置窗口控制器。
type GUI struct {
	deps Deps
	win  fyne.Window

	mu        sync.Mutex // 保护 cfg/st/statusMap（轮询 goroutine 与主线程）
	cfg       *config.Config
	st        *model.SyncStatus
	statusMap map[string]model.EntryStatus
	activeRow int

	table *widget.Table

	// 全局设置表单控件。
	gInterval *widget.Entry
	gDNSMode  *widget.Select
	gDOH      *widget.Entry
	gIPVer    *widget.Select
	gFlush    *widget.Check
	gPause    *widget.Check

	// 状态面板。
	stLabels map[string]*widget.Label
	logList  *widget.List
	logLines []string
	logMtime time.Time
	logSize  int64

	banner *widget.RichText

	// 立即同步（DSD §4.5 时序状态）。
	syncBtn           *widget.Button
	syncReqPending    bool
	syncReqAt         time.Time
	syncReqUpdatedAt  time.Time
	syncCooldownUntil time.Time

	watcher  *statusWatcher
	stopPoll chan struct{}
	stopOnce sync.Once
}

// New 构建配置窗口（未显示）。App 由调用方注入：生产传 app.NewWithID，
// 测试传 fyne/test.NewApp（DSD §7.1：fyne 仅渲染配置窗口，tray 独立管理）。
func New(deps Deps) *GUI {
	g := &GUI{
		deps:      deps,
		cfg:       config.Default(),
		statusMap: make(map[string]model.EntryStatus),
		stLabels:  make(map[string]*widget.Label),
		stopPoll:  make(chan struct{}),
		watcher:   newStatusWatcher(deps.statusPath()),
	}
	g.loadConfigIntoMemory()
	g.win = deps.App.NewWindow(WindowTitle)
	g.win.SetContent(g.buildContent())
	g.win.Resize(fyne.NewSize(1080, 640))
	return g
}

// Window 返回底层窗口（协调者控制 Show/Close/CloseIntercept）。
func (g *GUI) Window() fyne.Window { return g.win }

// ShowOrFocus 显示窗口；已显示则聚焦（托盘单击/菜单"打开配置"入口，
// 协调者负责经 fyne.Do 入主循环调用）。
func (g *GUI) ShowOrFocus() {
	if g.win == nil {
		return
	}
	if g.win.Content() == nil || !g.win.Content().Visible() {
		g.win.Show()
	} else {
		g.win.RequestFocus()
	}
}

// setActiveRow 记录当前活动数据行（工具栏操作的锚点）。
func (g *GUI) setActiveRow(row int) {
	g.activeRow = row
}

// ---------------------------------------------------------------------------
// 组件树装配（DSD §4.1）
// ---------------------------------------------------------------------------

const (
	colEnable = 0
	colSource = 1
	colTarget = 2
	colStatus = 3
	colIPs    = 4
	colNote   = 5
	numCols   = 6
)

// buildContent 组装 Border(top=Toolbar, bottom=StatusBar, center=Tabs)。
func (g *GUI) buildContent() fyne.CanvasObject {
	top := g.buildToolbar()
	center := g.buildTabs()
	g.banner = widget.NewRichText()
	g.banner.Truncation = fyne.TextTruncateEllipsis
	statusBar := container.NewHBox(
		wrapStatus(g.statusField(stSyncTime, "最近同步: --")),
		wrapStatus(g.statusField(stNextTime, "下次同步: --")),
		g.banner,
	)
	return container.NewBorder(top, statusBar, nil, nil, center)
}

// wrapStatus 状态栏字段限定宽度（避免挤压横幅）。
func wrapStatus(l *widget.Label) fyne.CanvasObject {
	return l
}

// buildToolbar 顶部工具栏：条目 CRUD/排序/启停 ｜ 立即同步。
func (g *GUI) buildToolbar() fyne.CanvasObject {
	g.syncBtn = widget.NewButtonWithIcon("立即同步", theme.MediaPlayIcon(), g.onSyncNow)
	return container.NewHBox(
		widget.NewButtonWithIcon("新增", theme.ContentAddIcon(), g.onAddEntry),
		widget.NewButtonWithIcon("编辑", theme.DocumentCreateIcon(), g.onEditEntry),
		widget.NewButtonWithIcon("删除", theme.DeleteIcon(), g.onDeleteEntry),
		widget.NewButtonWithIcon("上移", theme.MoveUpIcon(), func() { g.moveEntry(-1) }),
		widget.NewButtonWithIcon("下移", theme.MoveDownIcon(), func() { g.moveEntry(1) }),
		widget.NewButtonWithIcon("启用/停用", theme.VisibilityIcon(), g.onToggleEnabled),
		g.syncBtn,
	)
}

// buildTabs 中部 Tabs：条目 / 全局设置 / 状态。
func (g *GUI) buildTabs() fyne.CanvasObject {
	return container.NewAppTabs(
		container.NewTabItem("条目", g.buildTable()),
		container.NewTabItem("全局设置", g.buildSettings()),
		container.NewTabItem("状态", g.buildStatusPanel()),
	)
}

// ---------------------------------------------------------------------------
// 配置内存管理
// ---------------------------------------------------------------------------

// loadConfigIntoMemory 读取 config.yaml 进内存镜像（失败回落默认 + 状态栏横幅）。
func (g *GUI) loadConfigIntoMemory() {
	cfg, err := config.Load(g.deps.ConfigPath)
	if err != nil {
		g.cfg = config.Default()
		g.showBanner("config 读取失败（已载入默认）: "+err.Error(), bannerErr)
		return
	}
	cfg.Normalize()
	g.cfg = cfg
}

// saveConfig 将内存配置落盘（原子写）；失败显示横幅。
func (g *GUI) saveConfig() error {
	g.mu.Lock()
	cfg := g.cfg
	g.mu.Unlock()
	if err := cfg.Save(g.deps.ConfigPath); err != nil {
		g.showBanner("config 保存失败: "+err.Error(), bannerErr)
		return err
	}
	return nil
}

// ---------------------------------------------------------------------------
// 生命周期（轮询）
// ---------------------------------------------------------------------------

// StartPolling 启动 10s 状态轮询（生产由协调者在窗口 Show 后调用）：
// 读 status.json + 状态面板 + 日志尾部 + 立即同步反馈。
func (g *GUI) StartPolling() {
	go func() {
		tick := time.NewTicker(statusPollInterval)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				g.pollOnce()
			case <-g.stopPoll:
				return
			}
		}
	}()
}

// StopPolling 停止轮询（幂等；窗口关闭时调用）。
func (g *GUI) StopPolling() {
	g.stopOnce.Do(func() { close(g.stopPoll) })
}

// pollOnce 单次轮询：mtime 变化才重解析，容忍 rename 瞬时失败重试一次。
// 由轮询 goroutine 调用，控件更新经 fyne.Do 入主循环。
func (g *GUI) pollOnce() {
	st, changed, err := g.watcher.poll()
	if err != nil {
		return // 解析失败且重试仍失败：保持旧状态
	}
	if !changed {
		return
	}
	g.mu.Lock()
	updatedAt := time.Time{}
	if st != nil {
		updatedAt = st.UpdatedAt
	}
	g.st = st
	if st != nil {
		g.statusMap = indexStatus(st)
	} else {
		g.statusMap = make(map[string]model.EntryStatus)
	}
	g.mu.Unlock()

	fyne.Do(func() {
		g.refreshStatusPanel()
		g.refreshEntryCellsStatus()
		g.updateSyncFeedback(updatedAt)
	})
}

package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/model"
	"github.com/heliopause/ddns-hosts-sync/internal/state"
)

// ---------------------------------------------------------------------------
// fake traySvc：headless 下断言托盘刷新/菜单逻辑（真实 systray 循环需桌面环境）
// ---------------------------------------------------------------------------

type fakeMenuItem struct {
	clicked    chan struct{}
	checked    bool
	checkCalls int
}

func (f *fakeMenuItem) Clicked() <-chan struct{} { return f.clicked }
func (f *fakeMenuItem) Check(checked bool) {
	f.checked = checked
	f.checkCalls++
}

type fakeSvc struct {
	menus     []*fakeMenuItem
	menuSpecs []MenuSpec // 记录的 AddItem 入参
	iconSizes []int      // 每次 SetIcon 的字节数
	icons     [][]byte
	tooltips  []string
	titles    []string
	tapped    func()
	rebuilt   int
}

func (f *fakeSvc) Run(onReady, onExit func()) { onReady() }
func (f *fakeSvc) Quit()                      {}
func (f *fakeSvc) ResetMenu()                 { f.rebuilt++; f.menus = nil; f.menuSpecs = nil }
func (f *fakeSvc) AddSeparator()              { f.menuSpecs = append(f.menuSpecs, MenuSpec{Separator: true}) }
func (f *fakeSvc) AddItem(title, tooltip string, checkable, checked bool) MenuHandle {
	mi := &fakeMenuItem{clicked: make(chan struct{}, 8), checked: checked}
	f.menus = append(f.menus, mi)
	f.menuSpecs = append(f.menuSpecs, MenuSpec{ID: fmt.Sprintf("item-%d", len(f.menus)), Label: title, Checkable: checkable, Checked: checked})
	return mi
}
func (f *fakeSvc) SetIcon(png []byte) {
	f.iconSizes = append(f.iconSizes, len(png))
	f.icons = append(f.icons, png)
}
func (f *fakeSvc) SetTitle(title string)  { f.titles = append(f.titles, title) }
func (f *fakeSvc) SetTooltip(text string) { f.tooltips = append(f.tooltips, text) }
func (f *fakeSvc) SetOnTapped(fn func())  { f.tapped = fn }
func (f *fakeSvc) lastTooltip() string {
	if len(f.tooltips) == 0 {
		return ""
	}
	return f.tooltips[len(f.tooltips)-1]
}
func (f *fakeSvc) iconFor(marker string) ([]byte, error) {
	return trayIcon(ColorGreen) // 测试中只断言非空
}

func newFakeTray(t *testing.T, dir string, deps Deps) (*Tray, *fakeSvc) {
	t.Helper()
	if deps.LockPath == "" {
		deps.LockPath = filepath.Join(dir, "tray.lock")
	}
	if deps.ConfigPath == "" {
		deps.ConfigPath = filepath.Join(dir, "config", "config.yaml")
	}
	if deps.StatusPath == "" {
		deps.StatusPath = filepath.Join(dir, "state", "status.json")
	}
	if deps.TriggerPath == "" {
		deps.TriggerPath = filepath.Join(dir, "state", "trigger.json")
	}
	tr := New(deps)
	fake := &fakeSvc{}
	tr.svc = fake // 替换生产命令面
	return tr, fake
}

// writeStatus 便捷落盘支撑数据（保证目录存在）。
func writeStatus(t *testing.T, path string, s *model.SyncStatus) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := state.WriteStatus(path, s); err != nil {
		t.Fatal(err)
	}
}

func TestTrayOnReadyAndRefresh(t *testing.T) {
	dir := t.TempDir()
	now := time.Now().UTC()
	tr, fake := newFakeTray(t, dir, Deps{})

	// onReady：置灰 + 构建菜单 + 订阅单击。
	tr.onReady()
	if len(fake.iconSizes) == 0 {
		t.Fatal("onReady 应设置初始图标")
	}
	if fake.lastTooltip() == "" {
		t.Fatal("onReady 应设置初始 tooltip")
	}
	if fake.rebuilt != 1 {
		t.Fatalf("onReady 应重建菜单一次，实际 %d", fake.rebuilt)
	}
	if fake.tapped == nil {
		t.Fatal("onReady 应订阅图标单击")
	}

	// 无 status 文件时 refresh 维持灰（不崩溃、不换色）。
	fake.iconSizes = nil
	tr.refresh()
	if len(fake.iconSizes) != 0 {
		t.Fatal("status 缺失（nil）不应触发换色")
	}

	// 写入全绿 status → refresh 切绿。
	writeStatus(t, tr.deps.StatusPath, baseStatus(now))
	tr.color = ColorGray // 模拟初始灰
	fake.iconSizes = nil
	fake.tooltips = nil
	tr.refresh()
	if tr.color != ColorGreen {
		t.Fatalf("refresh 后色 = %v, want green", tr.color)
	}
	if len(fake.iconSizes) != 1 {
		t.Fatalf("切色应 SetIcon 一次，实际 %d", len(fake.iconSizes))
	}
	if tt := fake.lastTooltip(); !strings.Contains(tt, "最近同步") {
		t.Fatalf("tooltip 应含最近同步时间，得 %q", tt)
	}

	// 幂等：颜色未变不再 SetIcon。
	fake.iconSizes = nil
	tr.refresh()
	if len(fake.iconSizes) != 0 {
		t.Fatal("颜色未变不应重复 SetIcon")
	}

	// 写失败 status → 切红。
	bad := baseStatus(now)
	bad.HostsBlock.LastWriteOK = false
	writeStatus(t, tr.deps.StatusPath, bad)
	fake.iconSizes = nil
	tr.refresh()
	if tr.color != ColorRed {
		t.Fatalf("写失败应切红，得 %v", tr.color)
	}
}

func TestTrayRebuildMenuBindings(t *testing.T) {
	dir := t.TempDir()
	tr, fake := newFakeTray(t, dir, Deps{})

	tr.rebuildMenu(false)
	if fake.rebuilt != 1 {
		t.Fatalf("重建次数 = %d", fake.rebuilt)
	}
	// 菜单项数量：5 项 + 2 分隔符（规格长度 7）。
	if len(fake.menuSpecs) != len(BuildMenuSpec(false)) {
		t.Fatalf("菜单项数 = %d, want %d", len(fake.menuSpecs), len(BuildMenuSpec(false)))
	}
	// 暂停项句柄已登记且未勾选。
	h, ok := tr.items[MenuPauseToggle]
	if !ok {
		t.Fatal("暂停项句柄未登记")
	}
	if h.(*fakeMenuItem).checked {
		t.Fatal("未暂停时勾选应为空")
	}

	// 点击"打开配置"→ 分发到 deps.OpenConfig。
	opened := false
	tr.deps.OpenConfig = func() { opened = true }
	tr.dispatch(MenuOpenConfig)
	if !opened {
		t.Fatal("OpenConfig 回调未被调用")
	}
}

func TestTraySyncPauseItem(t *testing.T) {
	dir := t.TempDir()
	tr, _ := newFakeTray(t, dir, Deps{})
	tr.rebuildMenu(false)

	fi := tr.items[MenuPauseToggle].(*fakeMenuItem)
	tr.paused = true
	tr.syncPauseItem()
	if fi.checkCalls != 1 || !fi.checked {
		t.Fatalf("暂停态应勾选菜单项，得 checked=%v calls=%d", fi.checked, fi.checkCalls)
	}
}

func TestTrayTogglePausePersistsConfig(t *testing.T) {
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := os.MkdirAll(filepath.Dir(cfgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := config.Default()
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}

	tr, _ := newFakeTray(t, dir, Deps{ConfigPath: cfgPath})
	tr.rebuildMenu(false)

	tr.togglePause() // true → false
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Enabled {
		t.Fatal("togglePause 后 enabled 应为 false 且落盘")
	}
	if !tr.paused {
		t.Fatal("Tray.paused 应同步为 true")
	}

	tr.togglePause() // false → true
	reloaded, err = config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Enabled {
		t.Fatal("二次切换应恢复 enabled=true")
	}
}

func TestTraySyncNowTrigger(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	tr, _ := newFakeTray(t, dir, Deps{})
	// newFakeTray 注入的 TriggerPath 与状态目录一致；调用前确保目录存在
	//（生产环境由 install 创建 state/，托盘写入前防御性创建见 syncNow）。

	called := 0
	tr.deps.TriggerTask = func() { called++ }

	tr.syncNow()
	if called != 1 {
		t.Fatalf("触发回调调用数 = %d, want 1", called)
	}
	tg, ok, err := state.ReadJSON[*model.Trigger](tr.deps.TriggerPath)
	if err != nil || !ok {
		t.Fatalf("trigger 未落盘: ok=%v err=%v", ok, err)
	}
	if tg.Action != model.ActionSyncNow || tg.Version != model.StatusVersion {
		t.Fatalf("trigger 字段不符: %+v", tg)
	}
	// request_id 必须满足消费归档命名约束（[A-Za-z0-9-]）。
	for _, c := range tg.RequestID {
		if !((c >= 'a' && c <= 'z') || (c >= '0' && c <= '9') || c == '-') {
			t.Fatalf("request_id 含非法字符 %q", c)
		}
	}
	// 连续两次构造 request_id 不重复。
	tg2, _ := newSyncNowTrigger(time.Now().UTC())
	if tg2.RequestID == tg.RequestID {
		t.Fatal("两次触发 request_id 不得重复")
	}
}

func TestNewSyncNowTriggerShape(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	tg, ok := newSyncNowTrigger(now)
	if !ok {
		t.Fatal("构造应成功")
	}
	if !tg.RequestedAt.Equal(now) {
		t.Fatalf("RequestedAt = %v, want %v", tg.RequestedAt, now)
	}
	if !strings.HasPrefix(tg.RequestID, "tray-") {
		t.Fatalf("request_id 前缀异常: %q", tg.RequestID)
	}
}

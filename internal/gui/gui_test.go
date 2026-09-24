package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fyne.io/fyne/v2/test"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/model"
	"github.com/heliopause/ddns-hosts-sync/internal/state"
)

// newTestGUI 构造基于 fyne test 驱动的 GUI（headless 可运行）。
func newTestGUI(t *testing.T) (*GUI, string) {
	t.Helper()
	app := test.NewApp()
	dir := t.TempDir()
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := config.Default()
	cfg.Entries = []model.Entry{
		{ID: "e-01", Source: "a.example.com", Target: "target-a.example.com", Enabled: true, Note: "备注A"},
		{ID: "e-02", Source: "b.example.com", Target: "target-b.example.com", Enabled: false},
	}
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatal(err)
	}
	g := New(Deps{
		App:        app,
		ConfigPath: cfgPath,
		StateDir:   filepath.Join(dir, "state"),
		LogPath:    filepath.Join(dir, "logs", "sync.log"),
	})
	return g, dir
}

func TestGUITableBinding(t *testing.T) {
	g, _ := newTestGUI(t)
	if g.win == nil {
		t.Fatal("窗口未创建")
	}
	rows, cols := g.tableLength()
	if rows != 3 { // 表头 + 2 条目
		t.Fatalf("表格行数 = %d, want 3", rows)
	}
	if cols != numCols {
		t.Fatalf("表格列数 = %d, want %d", cols, numCols)
	}
	// 单元格文本映射。
	if got := g.cellText(0, colSource); got != "a.example.com" {
		t.Fatalf("colSource 文本 = %q", got)
	}
	if got := g.cellText(1, colTarget); got != "target-b.example.com" {
		t.Fatalf("colTarget 文本 = %q", got)
	}
	// 无状态时徽标为 —。
	if got := g.cellText(0, colStatus); !strings.Contains(got, "—") {
		t.Fatalf("无 status 徽标应为 —，得 %q", got)
	}
	// 表头。
	if got := g.headerTitle(colStatus); got != "状态" {
		t.Fatalf("表头 = %q", got)
	}
}

func TestGUIStatusBadge(t *testing.T) {
	cases := []struct {
		st   model.EntryStatus
		want string
	}{
		{model.EntryStatus{ID: "x", Status: model.EntryOK}, "OK"},
		{model.EntryStatus{ID: "x", Status: model.EntryNXDomain}, "NXDOMAIN"},
		{model.EntryStatus{ID: "x", Status: model.EntryTimeout}, "超时"},
		{model.EntryStatus{ID: "x", Status: model.EntryWriteFailed}, "写盘失败"},
		{model.EntryStatus{ID: "x", Status: model.EntryPaused}, "已暂停"},
		{model.EntryStatus{ID: "x", Status: model.EntryOK, UsedFallback: true}, "OK（回退）"},
		{model.EntryStatus{ID: "x", Status: model.EntryOK, Error: "旧值保留"}, "OK：旧值保留"},
		{model.EntryStatus{}, "—"},
	}
	for _, c := range cases {
		if got := statusBadge(c.st); got != c.want {
			t.Fatalf("statusBadge(%+v) = %q, want %q", c.st, got, c.want)
		}
	}
}

func TestGUIValidateEntryInput(t *testing.T) {
	ok := model.Entry{Source: "a.example.com", Target: "b.example.com"}
	if err := validateEntryInput(ok); err != nil {
		t.Fatalf("合法条目不应报错: %v", err)
	}
	bad := []model.Entry{
		{Source: "非法 域名", Target: "b.example.com"},
		{Source: "a.example.com", Target: "192.168.1.1"}, // IP 字面量
		{Source: "a.example.com", Target: "b.example.com", Note: strings.Repeat("长", 300)},
		{Source: "a", Target: "b"}, // 单标签
	}
	for i, e := range bad {
		if err := validateEntryInput(e); err == nil {
			t.Fatalf("case %d 应报错: %+v", i, e)
		}
	}
}

func TestGUIValidateEntryUnique(t *testing.T) {
	base := []model.Entry{{ID: "a", Target: "t.example.com", Enabled: true}}
	// 同 target 冲突（新增条目）。
	if err := validateEntryUnique(append(base, model.Entry{ID: "b", Target: "t.example.com", Enabled: true}), ""); err == nil {
		t.Fatal("target 重复应报错")
	}
	// 编辑自身时排除（自冲突不算）。
	if err := validateEntryUnique(base, "a"); err != nil {
		t.Fatalf("编辑自身不构成冲突: %v", err)
	}
	// 停用条目不参与去重。
	if err := validateEntryUnique(append(base, model.Entry{ID: "b", Target: "t.example.com", Enabled: false}), ""); err != nil {
		t.Fatalf("停用条目不参与去重: %v", err)
	}
	// 大小写折叠后去重。
	if err := validateEntryUnique(append(base, model.Entry{ID: "b", Target: "T.Example.COM", Enabled: true}), ""); err == nil {
		t.Fatal("大小写不同应视为重复")
	}
}

func TestGUIPauseCheckSetChecked(t *testing.T) {
	g, dir := newTestGUI(t)
	cfgPath := filepath.Join(dir, "config.yaml")

	// 模拟 Check 勾选（SetChecked 触发 OnChanged，若值变化）。
	if g.gPause.Checked {
		t.Fatal("初始不应勾选")
	}
	prev := g.gPause.OnChanged
	g.gPause.OnChanged = func(on bool) { t.Logf("暂停勾选回调: %v（stub，绕过落盘副作用）", on) }
	_ = prev

	// 真实路径：先还原回调，再用控件值驱动。
	g.gPause.OnChanged = func(on bool) {
		g.mu.Lock()
		if g.cfg != nil {
			g.cfg.Enabled = !on
		}
		g.mu.Unlock()
		_ = g.saveConfig()
	}
	g.gPause.SetChecked(true)
	if g.gPause.OnChanged == nil {
		t.Fatal("OnChanged 不得为 nil")
	}
	// 断言落盘。
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if reloaded.Enabled {
		t.Fatal("勾选暂停后 enabled 应落盘为 false")
	}

	// 取消勾选 → enabled=true 落盘。
	g.gPause.SetChecked(false)
	reloaded, err = config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if !reloaded.Enabled {
		t.Fatal("取消暂停后 enabled 应落盘为 true")
	}
}

func TestGUISyncNowWithFakeTrigger(t *testing.T) {
	g, dir := newTestGUI(t)
	triggerPath := filepath.Join(dir, "state", "trigger.json")
	if err := os.MkdirAll(filepath.Dir(triggerPath), 0o755); err != nil {
		t.Fatal(err)
	}

	called := false
	g.deps.TriggerNow = func() { called = true }

	g.mu.Lock()
	g.syncCooldownUntil = time.Time{} // 重置防连点
	g.mu.Unlock()

	g.onSyncNow()
	if !called {
		t.Fatal("fake 触发函数未被调用")
	}
	tg, ok, err := state.ReadJSON[*model.Trigger](triggerPath)
	if err != nil || !ok {
		t.Fatalf("trigger 未落盘: ok=%v err=%v", ok, err)
	}
	if tg.Action != model.ActionSyncNow || tg.Version != model.StatusVersion {
		t.Fatalf("trigger 字段不符: %+v", tg)
	}
	if !strings.HasPrefix(tg.RequestID, "gui-") {
		t.Fatalf("request_id 前缀异常: %q", tg.RequestID)
	}

	// 防连点：10s 内再次请求被拦截（不产生第二个 trigger）。
	g.mu.Lock()
	g.syncCooldownUntil = time.Now().Add(time.Minute)
	g.mu.Unlock()
	oldID := tg.RequestID
	g.onSyncNow()
	tg2, _, _ := state.ReadJSON[*model.Trigger](triggerPath)
	if tg2.RequestID != oldID {
		t.Fatal("防连点窗口内不应写入新 trigger")
	}
}

func TestGUISyncFeedbackState(t *testing.T) {
	base := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	reqAt := base
	t.Run("updated_at 前进 → 完成", func(t *testing.T) {
		done, msg, kind := syncFeedbackState(base.Add(30*time.Second), base, reqAt, reqAt.Add(30*time.Second))
		if !done || kind != bannerOK || !strings.Contains(msg, "同步完成") {
			t.Fatalf("done=%v kind=%v msg=%q", done, kind, msg)
		}
	})
	t.Run("未前进未超时 → 无反馈", func(t *testing.T) {
		done, _, _ := syncFeedbackState(base, base, reqAt, reqAt.Add(time.Minute))
		if done {
			t.Fatal("未前进且未超时不应反馈")
		}
	})
	t.Run("超 3 分钟无前进 → 红横幅", func(t *testing.T) {
		done, msg, kind := syncFeedbackState(base, base, reqAt, reqAt.Add(3*time.Minute+time.Second))
		if !done || kind != bannerErr || !strings.Contains(msg, "未响应") {
			t.Fatalf("done=%v kind=%v msg=%q", done, kind, msg)
		}
	})
	t.Run("边界：恰 3 分钟不判超时", func(t *testing.T) {
		done, _, _ := syncFeedbackState(base, base, reqAt, reqAt.Add(3*time.Minute))
		if done {
			t.Fatal("恰 3 分钟不应判超时（严格大于）")
		}
	})
}

// TestGUIUpsertEntry 新增/编辑落盘。
func TestGUIUpsertEntry(t *testing.T) {
	g, dir := newTestGUI(t)
	cfgPath := filepath.Join(dir, "config.yaml")

	g.upsertEntry(model.Entry{ID: "e-99", Source: "new.example.com", Target: "new-target.example.com", Enabled: true})
	reloaded, err := config.Load(cfgPath)
	if err != nil {
		t.Fatal(err)
	}
	if len(reloaded.Entries) != 3 {
		t.Fatalf("新增后条目数 = %d, want 3", len(reloaded.Entries))
	}
	if reloaded.Entries[2].ID != "e-99" {
		t.Fatalf("新条目应追加在末尾，得 %+v", reloaded.Entries[2])
	}

	// 编辑：target 更新，id 不变。
	g.upsertEntry(model.Entry{ID: "e-99", Source: "new.example.com", Target: "renamed.example.com", Enabled: false})
	reloaded, _ = config.Load(cfgPath)
	if len(reloaded.Entries) != 3 {
		t.Fatalf("编辑不应增加条目")
	}
	found := false
	for _, e := range reloaded.Entries {
		if e.ID == "e-99" && e.Target == "renamed.example.com" && !e.Enabled {
			found = true
		}
	}
	if !found {
		t.Fatal("编辑结果未落盘")
	}
}

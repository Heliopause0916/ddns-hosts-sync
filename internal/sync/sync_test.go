package sync_test

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/hostsfile"
	"github.com/heliopause/ddns-hosts-sync/internal/model"
	"github.com/heliopause/ddns-hosts-sync/internal/resolver"
	"github.com/heliopause/ddns-hosts-sync/internal/state"
	"github.com/heliopause/ddns-hosts-sync/internal/sync"
)

// ---------------------------------------------------------------------------
// 测试桩：可注入解析器
// ---------------------------------------------------------------------------

// fakeResolver 按 source 分发结果：okMap 记录解析成功（→ 固定 IP），
// failMap 记录 sentinel 失败。
type fakeResolver struct {
	bySource map[string][]string // source → IP 列表（成功）
	fail     map[string]error    // source → sentinel 错误
	calls    int
}

func (f *fakeResolver) resolve(source string, cfg model.DNSConfig) (*resolver.ResolveResult, error) {
	f.calls++
	if err, ok := f.fail[source]; ok {
		return nil, err
	}
	ips, ok := f.bySource[source]
	if !ok {
		return nil, resolver.ErrNXDomain
	}
	return &resolver.ResolveResult{
		IPs:          ips,
		Chain:        []string{source},
		FinalName:    source,
		UsedFallback: false,
		ResolvedAt:   time.Now().UTC(),
	}, nil
}

// ---------------------------------------------------------------------------
// 基础设施
// ---------------------------------------------------------------------------

type env struct {
	dir       string
	cfgPath   string
	hostsPath string
	status    string
	opts      sync.Options
}

func setupEnv(t *testing.T) *env {
	t.Helper()
	dir := t.TempDir()
	c := &env{
		dir:       dir,
		cfgPath:   filepath.Join(dir, "config.yaml"),
		hostsPath: filepath.Join(dir, "hosts"),
		status:    filepath.Join(dir, "state", "status.json"),
		opts: sync.Options{
			ConfigPath:   filepath.Join(dir, "config.yaml"),
			StateDir:     filepath.Join(dir, "state"),
			LogPath:      filepath.Join(dir, "logs", "sync.log"),
			HostsPath:    filepath.Join(dir, "hosts"),
			Force:        true,
			IntervalTick: 60,
		},
	}
	return c
}

func (e *env) writeConfig(t *testing.T, entries []model.Entry) {
	t.Helper()
	cfg := config.Default()
	cfg.Entries = entries
	cfg.FlushDNS = false // 单测不触发系统命令
	cfg.DNS.Mode = "system"
	if err := cfg.Save(e.cfgPath); err != nil {
		t.Fatalf("写配置失败: %v", err)
	}
}

func (e *env) attach(f *fakeResolver) {
	e.opts.Resolve = f.resolve
}

func (e *env) hostsContent(t *testing.T) []byte {
	t.Helper()
	c, err := os.ReadFile(e.hostsPath)
	if err != nil {
		t.Fatalf("读 hosts 失败: %v", err)
	}
	return c
}

func (e *env) hostsMTime(t *testing.T) time.Time {
	t.Helper()
	info, err := os.Stat(e.hostsPath)
	if err != nil {
		t.Fatalf("stat hosts 失败: %v", err)
	}
	return info.ModTime()
}

func (e *env) readStatus(t *testing.T) *model.SyncStatus {
	t.Helper()
	st, ok, err := state.ReadStatus(e.status)
	if err != nil || !ok {
		t.Fatalf("读 status 失败 (ok=%v): %v", ok, err)
	}
	return st
}

func (e *env) statusFor(t *testing.T, id string) model.EntryStatus {
	t.Helper()
	for _, es := range e.readStatus(t).Entries {
		if es.ID == id {
			return es
		}
	}
	t.Fatalf("status 中无条目 %s", id)
	return model.EntryStatus{}
}

var (
	e1 = model.Entry{ID: "e-01", Source: "ok-a.example.com", Target: "tgt-a.example.com", Enabled: true}
	e2 = model.Entry{ID: "e-02", Source: "ok-b.example.com", Target: "tgt-b.example.com", Enabled: true}
)

// ---------------------------------------------------------------------------
// 三路断言：变化→写盘、无变化→不写盘、失败→保留
// ---------------------------------------------------------------------------

func TestRun_ChangeWritesHosts(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10", "192.0.2.5"}}}
	e.attach(f)

	st, err := sync.Run(e.opts)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if st.SyncWindow != model.WindowSynced {
		t.Errorf("完整同步窗口应为 synced: %s", st.SyncWindow)
	}
	if !st.Task.LastRunOK {
		t.Errorf("LastRunOK 应为 true: %+v", st.Task)
	}
	if !st.HostsBlock.Present || st.HostsBlock.ContentMD5 != st.HostsBlock.ExpectedMD5 {
		t.Errorf("块应 present 且 content_md5==expected_md5: %+v", st.HostsBlock)
	}
	// hosts 出现标记块（IP 排序 v4 字典序）。
	content := string(e.hostsContent(t))
	if !strings.Contains(content, hostsfile.BeginMarker) || !strings.Contains(content, hostsfile.EndMarker) {
		t.Fatalf("hosts 缺标记块:\n%s", content)
	}
	for _, want := range []string{"192.0.2.5\ttgt-a.example.com", "192.0.2.10\ttgt-a.example.com"} {
		if !strings.Contains(content, want) {
			t.Errorf("hosts 缺行 %q:\n%s", want, content)
		}
	}
	es := e.statusFor(t, "e-01")
	if es.Status != model.EntryOK || es.HostsPresent != true || len(es.ResolvedIPs) != 2 {
		t.Errorf("条目状态不符: %+v", es)
	}
}

func TestRun_NoChangeDoesNotWrite(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"198.51.100.9"}}}
	e.attach(f)

	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}
	mtime1 := e.hostsMTime(t)
	content1 := string(e.hostsContent(t))

	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("次轮 Run 失败: %v", err)
	}
	if mtime2 := e.hostsMTime(t); !mtime2.Equal(mtime1) {
		t.Errorf("无变化时 hosts mtime 必须不变: %v → %v", mtime1, mtime2)
	}
	if content2 := string(e.hostsContent(t)); content1 != content2 {
		t.Errorf("无变化时 hosts 内容必须不变")
	}
	st := e.readStatus(t)
	if st.HostsBlock.ContentMD5 != st.HostsBlock.ExpectedMD5 {
		t.Errorf("content_md5 应等于 expected_md5: %+v", st.HostsBlock)
	}
}

func TestRun_FailureKeepsOldValue(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1, e2})
	f := &fakeResolver{bySource: map[string][]string{
		"ok-a.example.com": {"192.0.2.10"},
		"ok-b.example.com": {"203.0.113.77"},
	}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}

	// b 条目改为 NXDOMAIN：旧行必须保留（failure_keep_old）。
	f.fail = map[string]error{"ok-b.example.com": resolver.ErrNXDomain}
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("次轮 Run 失败: %v", err)
	}
	content := string(e.hostsContent(t))
	if !strings.Contains(content, "203.0.113.77\ttgt-b.example.com") {
		t.Errorf("失败条目的旧行应保留:\n%s", content)
	}
	es := e.statusFor(t, "e-02")
	if es.Status != model.EntryNXDomain || es.HostsPresent != true {
		t.Errorf("失败条目状态不符: %+v", es)
	}
	if !e.readStatus(t).Task.LastRunOK {
		t.Error("DNS 失败不应翻转 LastRunOK（写盘未发生）")
	}
}

func TestRun_FailureNoWrite(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"198.51.100.9"}}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}
	mtime1 := e.hostsMTime(t)

	// a 条目超时 → 期望块与现块一致 → 不写盘。
	f.fail = map[string]error{"ok-a.example.com": resolver.ErrTimeout}
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("失败轮 Run 失败: %v", err)
	}
	if mtime2 := e.hostsMTime(t); !mtime2.Equal(mtime1) {
		t.Errorf("失败且旧值不变时应不写盘: %v → %v", mtime1, mtime2)
	}
	es := e.statusFor(t, "e-01")
	if es.Status != model.EntryTimeout || !strings.Contains(es.Error, "保留上一次条目") {
		t.Errorf("timeout 条目状态/文案不符: %+v", es)
	}
}

func TestRun_ChangeAfterStable(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10"}}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}
	mtime1 := e.hostsMTime(t)

	// IP 变化 → 写盘。
	f.bySource["ok-a.example.com"] = []string{"192.0.2.99"}
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("变化轮 Run 失败: %v", err)
	}
	if mtime2 := e.hostsMTime(t); mtime2.Equal(mtime1) {
		t.Error("IP 变化应触发写盘（mtime 前进）")
	}
	if content := string(e.hostsContent(t)); !strings.Contains(content, "192.0.2.99\ttgt-a.example.com") {
		t.Errorf("新 IP 应写入:\n%s", content)
	}
	st := e.readStatus(t)
	if st.HostsBlock.ContentMD5 != st.HostsBlock.ExpectedMD5 {
		t.Errorf("写后 content_md5 应等于 expected_md5: %+v", st.HostsBlock)
	}
}

// ---------------------------------------------------------------------------
// 间隔门 / trigger
// ---------------------------------------------------------------------------

func TestRun_IntervalGateWaiting(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10"}}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}
	mtime1 := e.hostsMTime(t)

	e.opts.Force = false
	st, err := sync.Run(e.opts)
	if err != nil {
		t.Fatalf("等待轮 Run 失败: %v", err)
	}
	if st.SyncWindow != model.WindowWaiting {
		t.Errorf("未到间隔应 waiting: %s", st.SyncWindow)
	}
	if mtime2 := e.hostsMTime(t); !mtime2.Equal(mtime1) {
		t.Error("等待窗口不得触碰 hosts")
	}
	if len(st.Entries) != 1 || st.Entries[0].Status != model.EntryOK {
		t.Errorf("等待窗口应保留上轮条目状态: %+v", st.Entries)
	}
}

func TestRun_TriggerSyncNowForcesFullSync(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10"}}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}

	// 写入新鲜 trigger，Force=false 也应完整同步。
	triggerPath := filepath.Join(e.dir, "state", "trigger.json")
	tr := &model.Trigger{Version: 1, RequestID: "req-1", RequestedAt: time.Now().UTC(), Action: model.ActionSyncNow}
	if err := state.WriteTrigger(triggerPath, tr); err != nil {
		t.Fatalf("写 trigger 失败: %v", err)
	}
	e.opts.Force = false
	st, err := sync.Run(e.opts)
	if err != nil {
		t.Fatalf("trigger 轮 Run 失败: %v", err)
	}
	if st.SyncWindow != model.WindowSynced {
		t.Errorf("新鲜 sync_now trigger 应完整同步: %s", st.SyncWindow)
	}
	if _, err := os.Stat(triggerPath); !os.IsNotExist(err) {
		t.Error("trigger 应被消费移除")
	}
}
func TestRun_StaleTriggerIgnored(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10"}}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}
	triggerPath := filepath.Join(e.dir, "state", "trigger.json")
	tr := &model.Trigger{Version: 1, RequestID: "req-old", RequestedAt: time.Now().Add(-10 * time.Minute), Action: model.ActionSyncNow}
	if err := state.WriteTrigger(triggerPath, tr); err != nil {
		t.Fatalf("写 trigger 失败: %v", err)
	}
	e.opts.Force = false
	mtime1 := e.hostsMTime(t)
	st, err := sync.Run(e.opts)
	if err != nil {
		t.Fatalf("过期 trigger 轮 Run 失败: %v", err)
	}
	if st.SyncWindow != model.WindowWaiting {
		t.Errorf("过期 trigger 不应强制同步: %s", st.SyncWindow)
	}
	if mtime2 := e.hostsMTime(t); !mtime2.Equal(mtime1) {
		t.Error("过期 trigger 不得触写盘")
	}
	if _, err := os.Stat(triggerPath); !os.IsNotExist(err) {
		t.Error("过期 trigger 应被消费移除")
	}
}

// TestRun_TriggerPathInConfigDir（B2）trigger 落点迁移：Options.TriggerPath
// 指向 config 目录时，触发由该路径消费，归档文件名语义（
// trigger.consumed.<requestID>.json）保留在 config 目录下。
func TestRun_TriggerPathInConfigDir(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	configDir := filepath.Join(e.dir, "config")
	if err := os.MkdirAll(configDir, 0o755); err != nil {
		t.Fatalf("建 config 目录失败: %v", err)
	}
	triggerPath := filepath.Join(configDir, "trigger.json")
	e.opts.TriggerPath = triggerPath
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10"}}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}

	// 在 config 目录写新鲜 trigger（模拟 GUI 新落点）。
	tr := &model.Trigger{Version: 1, RequestID: "req-b2", RequestedAt: time.Now().UTC(), Action: model.ActionSyncNow}
	if err := state.WriteTrigger(triggerPath, tr); err != nil {
		t.Fatalf("写 config 下 trigger 失败: %v", err)
	}
	e.opts.Force = false
	st, err := sync.Run(e.opts)
	if err != nil {
		t.Fatalf("trigger 轮 Run 失败: %v", err)
	}
	if st.SyncWindow != model.WindowSynced {
		t.Errorf("config 下新鲜 sync_now trigger 应完整同步: %s", st.SyncWindow)
	}
	if _, err := os.Stat(triggerPath); !os.IsNotExist(err) {
		t.Error("config 下 trigger 应被消费移除")
	}
	// 归档文件名语义保留：trigger.consumed.<requestID>.json 出现在 config 目录。
	archived := filepath.Join(configDir, "trigger.consumed.req-b2.json")
	if _, err := os.Stat(archived); !os.IsNotExist(err) {
		t.Errorf("归档文件不应残留在 config 目录（消费后即删除）: %v", err)
	}
}

// ---------------------------------------------------------------------------
// 配置类与条目降级
// ---------------------------------------------------------------------------

func TestRun_ConfigBrokenFallsBackToDefault(t *testing.T) {
	e := setupEnv(t)
	if err := os.WriteFile(e.cfgPath, []byte("::: not yaml :::"), 0o644); err != nil {
		t.Fatalf("写坏配置失败: %v", err)
	}
	st, err := sync.Run(e.opts)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if !strings.Contains(st.Task.LastError, "config 解析失败") {
		t.Errorf("last_error 应记录 config 解析失败: %q", st.Task.LastError)
	}
	if len(st.Entries) != 0 {
		t.Errorf("兜底默认配置 entries 应为空: %+v", st.Entries)
	}
	if st.IntervalMinutes != 5 {
		t.Errorf("默认间隔应为 5: %d", st.IntervalMinutes)
	}
}

func TestRun_InvalidConfigEntrySkipped(t *testing.T) {
	bad := model.Entry{ID: "e-bad", Source: "singlelabel", Target: "tgt.example.com", Enabled: true}
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{bad, e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10"}}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	es := e.statusFor(t, "e-bad")
	if es.Status != model.EntryInvalidConfig {
		t.Errorf("非法条目应 invalid_config: %+v", es)
	}
	ok := e.statusFor(t, "e-01")
	if ok.Status != model.EntryOK {
		t.Errorf("正常条目应 ok: %+v", ok)
	}
}

func TestRun_PausedEntryKeepsHostsLine(t *testing.T) {
	// 首轮 e1 解析成功写入行；次轮将其停用且 e2 变化触发写盘 → 停用条目旧行必须保留。
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1, e2})
	f := &fakeResolver{bySource: map[string][]string{
		"ok-a.example.com": {"192.0.2.10"},
		"ok-b.example.com": {"203.0.113.50"},
	}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}

	// 停用 e1，仅改 e2 的 IP。
	disabled := e1
	disabled.Enabled = false
	e.writeConfig(t, []model.Entry{disabled, e2})
	f.bySource["ok-b.example.com"] = []string{"203.0.113.51"}
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("次轮 Run 失败: %v", err)
	}
	es := e.statusFor(t, "e-01")
	if es.Status != model.EntryPaused {
		t.Errorf("停用条目应 paused: %+v", es)
	}
	content := string(e.hostsContent(t))
	if !strings.Contains(content, "192.0.2.10\ttgt-a.example.com") {
		t.Errorf("停用条目旧行应保留（不删除 hosts 已有行）:\n%s", content)
	}
	if !strings.Contains(content, "203.0.113.51\ttgt-b.example.com") {
		t.Errorf("变化条目新 IP 应写入:\n%s", content)
	}
}

// ---------------------------------------------------------------------------
// 块自愈 / 写失败
// ---------------------------------------------------------------------------

func TestRun_BlockTamperHeals(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10", "192.0.2.11"}}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}

	// 人为删除块内一行。
	tampered := strings.Replace(string(e.hostsContent(t)), "192.0.2.11\ttgt-a.example.com", "", 1)
	if err := os.WriteFile(e.hostsPath, []byte(tampered), 0o644); err != nil {
		t.Fatalf("篡改 hosts 失败: %v", err)
	}
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("自愈轮 Run 失败: %v", err)
	}
	content := string(e.hostsContent(t))
	if !strings.Contains(content, "192.0.2.11\ttgt-a.example.com") {
		t.Errorf("被删行应自愈写回:\n%s", content)
	}
	if _, err := os.Stat(filepath.Join(e.dir, "hosts.bak-ddns-hosts-sync")); err != nil {
		t.Errorf("自愈写盘应产生备份: %v", err)
	}
	st := e.readStatus(t)
	if st.HostsBlock.ContentMD5 != st.HostsBlock.ExpectedMD5 {
		t.Errorf("自愈后 md5 应一致: %+v", st.HostsBlock)
	}
}

func TestRun_WriteFailureRaisesWriteFailed(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10"}}}
	e.attach(f)
	// hosts 指向不存在的父目录 → WriteAtomic 必然失败（重试 3 次后放弃）。
	e.opts.HostsPath = filepath.Join(e.dir, "no-such-dir", "hosts")
	st, err := sync.Run(e.opts)
	if err != nil {
		t.Fatalf("Run 应返回 status 而非致命错误: %v", err)
	}
	if st.Task.LastRunOK {
		t.Error("写盘失败 LastRunOK 应为 false")
	}
	if st.HostsBlock.LastWriteOK {
		t.Error("写盘失败 LastWriteOK 应为 false")
	}
	es := e.statusFor(t, "e-01")
	if es.Status != model.EntryWriteFailed {
		t.Errorf("解析成功但写盘失败应 write_failed: %+v", es)
	}
}

// ---------------------------------------------------------------------------
// 致命错误路径
// ---------------------------------------------------------------------------

func TestRun_StateDirUnwritableIsFatal(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	e.opts.StateDir = filepath.Join(e.dir, "state", "cannot-create") // 存在“状态目录不可用”场景：以文件挡路径
	if err := os.WriteFile(filepath.Join(e.dir, "state"), []byte("x"), 0o644); err != nil {
		t.Fatalf("准备失败: %v", err)
	}
	_, err := sync.Run(e.opts)
	if err == nil {
		t.Fatal("状态目录不可用应返回致命错误")
	}
	if !strings.Contains(err.Error(), "状态目录不可用") {
		t.Errorf("致命错误文案不符: %v", err)
	}
}

// 组合场景：时间回拨（lastRunAt 在未来 → 视同未到点，等待窗口）。
func TestRun_ClockSkewAheadGates(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10"}}}
	e.attach(f)
	// 预写 status：last_run_at 在未来（时钟回拨后视为未到点）。
	if err := os.MkdirAll(filepath.Join(e.dir, "state"), 0o755); err != nil {
		t.Fatalf("创建 state 目录失败: %v", err)
	}
	st := &model.SyncStatus{
		Version:   model.StatusVersion,
		UpdatedAt: time.Now().UTC(),
		Task:      model.TaskStatus{LastRunAt: time.Now().Add(30 * time.Minute), LastRunOK: true},
		Entries:   []model.EntryStatus{{ID: "e-01", Enabled: true, Status: model.EntryOK}},
	}
	if err := state.WriteStatus(e.status, st); err != nil {
		t.Fatalf("预写 status 失败: %v", err)
	}
	e.opts.Force = false
	got, err := sync.Run(e.opts)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if got.SyncWindow != model.WindowWaiting {
		t.Errorf("时间回拨（last_run_at 在未来）应视为未到点等待: %s", got.SyncWindow)
	}
}

// ---------------------------------------------------------------------------
// M2a 补充测试
// ---------------------------------------------------------------------------

// TestRun_EnabledFalseSkipsDistribution（I-1）enabled=false 全局停用：
// 不触发任何解析与 hosts 写盘，仅写 waiting 最小 status（刷新 updated_at）。
func TestRun_EnabledFalseSkipsDistribution(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10"}}}
	e.attach(f)

	cfg := config.Default()
	cfg.Entries = []model.Entry{e1}
	cfg.Enabled = false
	cfg.FlushDNS = false
	cfg.DNS.Mode = "system"
	if err := cfg.Save(e.cfgPath); err != nil {
		t.Fatalf("写停用配置失败: %v", err)
	}
	st, err := sync.Run(e.opts)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if f.calls != 0 {
		t.Errorf("enabled=false 不应触发任何解析，实际 %d 次", f.calls)
	}
	if st.SyncWindow != model.WindowWaiting {
		t.Errorf("停用窗口应为 waiting: %s", st.SyncWindow)
	}
	if _, serr := os.Stat(e.hostsPath); !os.IsNotExist(serr) {
		t.Error("enabled=false 不得创建/触碰 hosts")
	}
	got := e.readStatus(t)
	if got.SyncWindow != model.WindowWaiting || got.UpdatedAt.IsZero() {
		t.Errorf("应落盘 waiting 最小 status 且刷新 updated_at: %+v", got)
	}
}

// TestRun_DeletedEntryRemovedFromHosts（I-2）删除 config 条目后重跑：
// 该 target 旧行随替换清走；保留条目不受影响。
func TestRun_DeletedEntryRemovedFromHosts(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1, e2})
	f := &fakeResolver{bySource: map[string][]string{
		"ok-a.example.com": {"192.0.2.10"},
		"ok-b.example.com": {"203.0.113.77"},
	}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}
	if content := string(e.hostsContent(t)); !strings.Contains(content, "203.0.113.77\ttgt-b.example.com") {
		t.Fatalf("前置：两条目均应写入:\n%s", content)
	}

	// 删除 e2 → 重跑 → b 行消失，a 行保留。
	e.writeConfig(t, []model.Entry{e1})
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("删除后 Run 失败: %v", err)
	}
	content := string(e.hostsContent(t))
	if strings.Contains(content, "tgt-b.example.com") {
		t.Errorf("已删除条目（target 不在 config）旧行应被清理:\n%s", content)
	}
	if !strings.Contains(content, "192.0.2.10\ttgt-a.example.com") {
		t.Errorf("保留条目行不应受影响:\n%s", content)
	}
}

// TestRun_EmptyEntriesEmptyHostsFirstRun 空 entries + 空 hosts 首次运行：
// 创建仅含两条 marker 的空块（present=true 可检测），无异常。
func TestRun_EmptyEntriesEmptyHostsFirstRun(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, nil) // 空条目
	st, err := sync.Run(e.opts)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if st.SyncWindow != model.WindowSynced || !st.Task.LastRunOK {
		t.Errorf("全同步应成功: %+v", st.Task)
	}
	content := e.hostsContent(t)
	if !strings.Contains(string(content), hostsfile.BeginMarker) || !strings.Contains(string(content), hostsfile.EndMarker) {
		t.Fatalf("空配置也应落两个 marker:\n%s", content)
	}
	if lines := hostsfile.ParseBlock(content); len(lines) != 0 {
		t.Errorf("空块应无内层行: %+v", lines)
	}
	if !st.HostsBlock.Present {
		t.Error("空块仍应 present=true")
	}
}

// TestRun_UppercaseEditedTargetSingleLine（S-8 e2e）手改大写 target 后重跑：
// 归一为 config 小写值，不出现大小写双行，块上一版为大写时自动自愈重写。
func TestRun_UppercaseEditedTargetSingleLine(t *testing.T) {
	e := setupEnv(t)
	e.writeConfig(t, []model.Entry{e1})
	f := &fakeResolver{bySource: map[string][]string{"ok-a.example.com": {"192.0.2.10"}}}
	e.attach(f)
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("首轮 Run 失败: %v", err)
	}
	tampered := strings.Replace(string(e.hostsContent(t)), "tgt-a.example.com", "TGT-A.EXAMPLE.COM", 1)
	if err := os.WriteFile(e.hostsPath, []byte(tampered), 0o644); err != nil {
		t.Fatalf("大写篡改失败: %v", err)
	}
	if _, err := sync.Run(e.opts); err != nil {
		t.Fatalf("归一轮 Run 失败: %v", err)
	}
	content := string(e.hostsContent(t))
	if strings.Contains(content, "TGT-A.EXAMPLE.COM") {
		t.Errorf("大写 target 应被归一替换:\n%s", content)
	}
	if n := strings.Count(content, "192.0.2.10\t"); n != 1 {
		t.Errorf("同一 target 不应出现双行（实际 %d 行）:\n%s", n, content)
	}
	if n := strings.Count(content, "\tTGT-A"); n != 0 {
		t.Errorf("不应残留大写行:\n%s", content)
	}
}

package sync

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/model"
	"github.com/heliopause/ddns-hosts-sync/internal/resolver"
)

// fakeResolve 内测用解析钩子：固定返回 192.0.2.1，避免真实 DoH 网络等待。
func fakeResolve(source string, _ model.DNSConfig) (*resolver.ResolveResult, error) {
	return &resolver.ResolveResult{
		IPs:        []string{"192.0.2.1"},
		Chain:      []string{source},
		FinalName:  source,
		ResolvedAt: time.Now().UTC(),
	}, nil
}

// TestFlushDNSCacheFailureIsReportedButNonFatal 已迁移至 internal/platform
// （TestFlushDNSCacheErrorsWhenUnavailable，语义保留）：sync 侧 flushdns 职责
// 自 M2b-1 起由 platform 注入（Options.FlushDNS），本包不再持有 OS 实现。

// ---------------------------------------------------------------------------
// I-3 sync.lock 单实例护栏
// ---------------------------------------------------------------------------

// TestAcquireLockNormalRelease 正常获取 → 释放后锁文件删除、可再次获取。
func TestAcquireLockNormalRelease(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")
	release, contended, err := acquireSyncLock(path, 500*time.Millisecond, time.Minute)
	if err != nil || contended {
		t.Fatalf("首次获取失败: contended=%v err=%v", contended, err)
	}
	release()
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Error("释放后锁文件应被删除")
	}
	release2, contended2, err := acquireSyncLock(path, 500*time.Millisecond, time.Minute)
	if err != nil || contended2 {
		t.Fatalf("释放后应能再次获取: contended=%v err=%v", contended2, err)
	}
	release2()
}

// TestAcquireLockStaleSelfHeals 陈旧锁（mtime 超阈值，进程必然已死）被删除
// 后重试一次即获取成功（O_EXCL 死锁自愈语义）。
func TestAcquireLockStaleSelfHeals(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.lock")
	if err := os.WriteFile(path, []byte("stale-owner"), 0o644); err != nil {
		t.Fatalf("预写陈旧锁: %v", err)
	}
	old := time.Now().Add(-11 * time.Minute)
	if err := os.Chtimes(path, old, old); err != nil {
		t.Fatalf("设置陈旧 mtime: %v", err)
	}
	release, contended, err := acquireSyncLock(path, 2*time.Second, 10*time.Minute)
	if err != nil || contended {
		t.Fatalf("陈旧锁应自愈获取: contended=%v err=%v", contended, err)
	}
	release()
}

// TestRunLockContendedReturnsNil 锁被他人持有时（非陈旧），Run 等待 lockWait
// 后返回 (nil, nil)（调用方据此 exit 0），不写 status、不触 hosts、不动他人锁。
func TestRunLockContendedReturnsNil(t *testing.T) {
	oldWait := lockWait
	lockWait = 100 * time.Millisecond
	defer func() { lockWait = oldWait }()

	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("建 state 目录: %v", err)
	}
	lockPath := filepath.Join(stateDir, "sync.lock")
	if err := os.WriteFile(lockPath, []byte("held-by-other"), 0o644); err != nil {
		t.Fatalf("预占锁: %v", err)
	}
	cfg := config.Default()
	cfg.Entries = []model.Entry{{ID: "e-01", Source: "a.example.com", Target: "t.example.com", Enabled: true}}
	cfg.FlushDNS = false
	cfgPath := filepath.Join(dir, "config.yaml")
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	opts := Options{
		ConfigPath:   cfgPath,
		StateDir:     stateDir,
		LogPath:      filepath.Join(dir, "logs", "sync.log"),
		HostsPath:    filepath.Join(dir, "hosts"),
		Force:        true,
		IntervalTick: 60,
	}
	st, err := Run(opts)
	if err != nil {
		t.Fatalf("锁冲突应返回 nil error（exit 0），实际 %v", err)
	}
	if st != nil {
		t.Errorf("锁冲突不应返回 status: %+v", st)
	}
	if _, serr := os.Stat(filepath.Join(stateDir, "status.json")); !os.IsNotExist(serr) {
		t.Error("锁冲突（未干活）不应写 status.json")
	}
	if _, serr := os.Stat(lockPath); serr != nil {
		t.Error("他人持有的锁不应被删除")
	}
}

// TestRunLockFreshLockAcquirableAfterRelease Run 正常结束释放锁 → 后续 Run
// 可再次获取并完整执行（无锁残留）。
func TestRunLockFreshLockAcquirableAfterRelease(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := config.Default()
	cfg.Entries = []model.Entry{{ID: "e-01", Source: "a.example.com", Target: "t.example.com", Enabled: true}}
	cfg.FlushDNS = false
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	opts := Options{
		ConfigPath:   cfgPath,
		StateDir:     stateDir,
		LogPath:      filepath.Join(dir, "logs", "sync.log"),
		HostsPath:    filepath.Join(dir, "hosts"),
		Force:        true,
		IntervalTick: 60,
		Resolve:      fakeResolve,
	}
	if _, err := Run(opts); err != nil {
		t.Fatalf("首次 Run 失败: %v", err)
	}
	if _, serr := os.Stat(filepath.Join(stateDir, "sync.lock")); !os.IsNotExist(serr) {
		t.Error("Run 结束后锁文件应被释放删除")
	}
	if _, err := Run(opts); err != nil {
		t.Fatalf("释放后二次 Run 失败（应可重新获取锁）: %v", err)
	}
}

// TestRunCleanupStaleTmp（S-1）Run 步骤 1 后清理 state 目录崩溃残留 tmp。
func TestRunCleanupStaleTmp(t *testing.T) {
	dir := t.TempDir()
	stateDir := filepath.Join(dir, "state")
	if err := os.MkdirAll(stateDir, 0o755); err != nil {
		t.Fatalf("建 state 目录: %v", err)
	}
	stale := filepath.Join(stateDir, "status.json.99.1.tmp")
	if err := os.WriteFile(stale, []byte("残骸"), 0o644); err != nil {
		t.Fatalf("写陈旧 tmp: %v", err)
	}
	old := time.Now().Add(-2 * time.Hour)
	if err := os.Chtimes(stale, old, old); err != nil {
		t.Fatalf("设陈旧 mtime: %v", err)
	}
	cfgPath := filepath.Join(dir, "config.yaml")
	cfg := config.Default()
	cfg.Entries = nil
	cfg.FlushDNS = false
	if err := cfg.Save(cfgPath); err != nil {
		t.Fatalf("写配置: %v", err)
	}
	opts := Options{
		ConfigPath:   cfgPath,
		StateDir:     stateDir,
		LogPath:      filepath.Join(dir, "logs", "sync.log"),
		HostsPath:    filepath.Join(dir, "hosts"),
		Force:        true,
		IntervalTick: 60,
	}
	if _, err := Run(opts); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if _, serr := os.Stat(stale); !os.IsNotExist(serr) {
		t.Error("陈旧崩溃残留 tmp 应被清理")
	}
}

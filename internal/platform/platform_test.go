package platform

import (
	"path/filepath"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 路径纯函数：三平台构造断言（基于注入 env 映射，任意平台可测）
// ---------------------------------------------------------------------------

// TestResolveWindowsPaths %SystemRoot%/%ProgramData% 注入 → 标准路径构造。
func TestResolveWindowsPaths(t *testing.T) {
	p := resolveWindowsPaths(map[string]string{
		"SystemRoot":  `C:\Windows`,
		"ProgramData": `C:\ProgramData`,
	})
	if p.hosts != `C:\Windows\System32\drivers\etc\hosts` {
		t.Errorf("hosts 路径不符: %q", p.hosts)
	}
	if p.dataDir != `C:\ProgramData\ddns-hosts-sync` {
		t.Errorf("数据目录不符: %q", p.dataDir)
	}
	if p.config != `C:\ProgramData\ddns-hosts-sync\config\config.yaml` {
		t.Errorf("配置路径不符: %q", p.config)
	}
	if p.state != `C:\ProgramData\ddns-hosts-sync\state` {
		t.Errorf("状态目录不符: %q", p.state)
	}
	if p.log != `C:\ProgramData\ddns-hosts-sync\logs\sync.log` {
		t.Errorf("日志路径不符: %q", p.log)
	}
}

// TestResolveWindowsPathsFallback 环境缺失（%SystemRoot% 已省略）→ 回落
// C:\Windows / C:\ProgramData。
func TestResolveWindowsPathsFallback(t *testing.T) {
	p := resolveWindowsPaths(map[string]string{})
	if !strings.HasPrefix(p.hosts, `C:\Windows`) {
		t.Errorf("SystemRoot 缺失应回落 C:\\Windows: %q", p.hosts)
	}
	if !strings.HasPrefix(p.dataDir, `C:\ProgramData`) {
		t.Errorf("ProgramData 缺失应回落 C:\\ProgramData: %q", p.dataDir)
	}
}

// TestResolveDarwinPaths 常量路径断言（ARCHITECTURE §5.2）。
func TestResolveDarwinPaths(t *testing.T) {
	p := resolveDarwinPaths(map[string]string{})
	if p.hosts != "/etc/hosts" {
		t.Errorf("hosts 路径不符: %q", p.hosts)
	}
	if p.dataDir != "/usr/local/var/ddns-hosts-sync" {
		t.Errorf("数据目录不符: %q", p.dataDir)
	}
	if p.config != "/usr/local/var/ddns-hosts-sync/config/config.yaml" {
		t.Errorf("配置路径不符: %q", p.config)
	}
}

// TestResolveLinuxPaths XDG_DATA_HOME 设置 → 以 XDG 为根。
func TestResolveLinuxPaths(t *testing.T) {
	p := resolveLinuxPaths(map[string]string{
		"XDG_DATA_HOME": "/srv/data",
	})
	want := "/srv/data/ddns-hosts-sync"
	if p.dataDir != want {
		t.Errorf("数据目录不符: %q", p.dataDir)
	}
	if p.config != filepath.Join(want, "config", "config.yaml") {
		t.Errorf("配置路径不符: %q", p.config)
	}
	if p.hosts != "/etc/hosts" {
		t.Errorf("hosts 路径不符: %q", p.hosts)
	}
}

// TestResolveLinuxPathsFallbackHome XDG_DATA_HOME 未设置 → 回落
// $HOME/.local/share/ddns-hosts-sync。
func TestResolveLinuxPathsFallbackHome(t *testing.T) {
	p := resolveLinuxPaths(map[string]string{"HOME": "/home/dev"})
	want := "/home/dev/.local/share/ddns-hosts-sync"
	if p.dataDir != want {
		t.Errorf("XDG 未设应回落 ~/.local/share：%q", p.dataDir)
	}
}

// ---------------------------------------------------------------------------
// NewWithDataDir rebasing：--dir 覆盖数据派生路径、hosts 保持平台默认
// ---------------------------------------------------------------------------

// TestRebaseWithDataDir --dir 注入后 config/state/log 全部落到 dir 下，
// hosts 不受影响（Linux 实测路径行为；rebasing 为三平台共享逻辑）。
func TestRebaseWithDataDir(t *testing.T) {
	p := NewWithDataDir("/tmp/ddns-test")
	if p.DataDir() != "/tmp/ddns-test" {
		t.Errorf("DataDir 应被覆盖: %q", p.DataDir())
	}
	if p.ConfigPath() != "/tmp/ddns-test/config/config.yaml" {
		t.Errorf("ConfigPath 应随 DataDir 重算: %q", p.ConfigPath())
	}
	if p.StateDir() != "/tmp/ddns-test/state" {
		t.Errorf("StateDir 应随 DataDir 重算: %q", p.StateDir())
	}
	if p.LogPath() != "/tmp/ddns-test/logs/sync.log" {
		t.Errorf("LogPath 应随 DataDir 重算: %q", p.LogPath())
	}
	if p.HostsPath() != "/etc/hosts" {
		t.Errorf("hosts 不应受 --dir 影响: %q", p.HostsPath())
	}
}

// TestFlushDNSCacheErrorsWhenUnavailable flushdns 兜底败路径（自 M1 的
// sync.flushDNSCache 迁移，语义保留）：PATH 无 resolvectl/systemd-resolve
// → 返回错误（调用方仅 warn）。
func TestFlushDNSCacheErrorsWhenUnavailable(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // 空 PATH，任何命令 LookPath 失败
	if err := New().FlushDNSCache(); err == nil {
		t.Error("无可用缓存刷新命令应返回错误")
	}
}

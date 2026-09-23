package model

import "testing"

// TestDefaultGlobalConfig 断言 DSD §1.1 默认值表。
func TestDefaultGlobalConfig(t *testing.T) {
	g := DefaultGlobalConfig()

	if g.Version != 1 {
		t.Errorf("Version = %d, 期望 1", g.Version)
	}
	if !g.Enabled {
		t.Error("Enabled = false, 期望 true")
	}
	if g.IntervalMinutes != 5 {
		t.Errorf("IntervalMinutes = %d, 期望 5", g.IntervalMinutes)
	}
	if g.DNS.Mode != "doh" {
		t.Errorf("DNS.Mode = %q, 期望 \"doh\"", g.DNS.Mode)
	}
	if len(g.DNS.DOHServers) != 2 {
		t.Fatalf("len(DOHServers) = %d, 期望 2", len(g.DNS.DOHServers))
	}
	if g.DNS.DOHServers[0] != "https://cloudflare-dns.com/dns-query" {
		t.Errorf("DOHServers[0] = %q, 期望 cloudflare DoH 端点", g.DNS.DOHServers[0])
	}
	if g.DNS.DOHServers[1] != "https://dns.google/resolve" {
		t.Errorf("DOHServers[1] = %q, 期望 dns.google DoH 端点", g.DNS.DOHServers[1])
	}
	if g.DNS.TimeoutSec != 15 {
		t.Errorf("DNS.TimeoutSec = %d, 期望 15", g.DNS.TimeoutSec)
	}
	if g.DNS.IPVersion != "ipv4" {
		t.Errorf("DNS.IPVersion = %q, 期望 \"ipv4\"", g.DNS.IPVersion)
	}
	if g.MaxCNAMEDepth != 10 {
		t.Errorf("MaxCNAMEDepth = %d, 期望 10", g.MaxCNAMEDepth)
	}
	if !g.FailureKeepOld {
		t.Error("FailureKeepOld = false, 期望 true（v1 锁定）")
	}
	if !g.FlushDNS {
		t.Error("FlushDNS = false, 期望 true")
	}
}

// TestStateEnumValues 冻结协议枚举常量值（DSD §1.3/§1.4/§1.5，字符串值即线格式）。
func TestStateEnumValues(t *testing.T) {
	// EntryState 8 值
	entryStates := map[string]EntryState{
		"ok":             EntryOK,
		"nxdomain":       EntryNXDomain,
		"cname_loop":     EntryCNAMELoop,
		"too_deep":       EntryTooDeep,
		"timeout":        EntryTimeout,
		"write_failed":   EntryWriteFailed,
		"paused":         EntryPaused,
		"invalid_config": EntryInvalidConfig,
	}
	if len(entryStates) != 8 {
		t.Fatalf("EntryState 枚举值数量 = %d, 期望 8", len(entryStates))
	}
	for want, got := range entryStates {
		if string(got) != want {
			t.Errorf("EntryState 常量 %q, 期望 %q", got, want)
		}
	}

	// TriggerAction 2 值
	if string(ActionSyncNow) != "sync_now" {
		t.Errorf("ActionSyncNow = %q, 期望 \"sync_now\"", ActionSyncNow)
	}
	if string(ActionReconfigure) != "reconfigure" {
		t.Errorf("ActionReconfigure = %q, 期望 \"reconfigure\"", ActionReconfigure)
	}

	// SyncWindowState 2 值
	if string(WindowWaiting) != "waiting" {
		t.Errorf("WindowWaiting = %q, 期望 \"waiting\"", WindowWaiting)
	}
	if string(WindowSynced) != "synced" {
		t.Errorf("WindowSynced = %q, 期望 \"synced\"", WindowSynced)
	}
}

// TestDefaultConfigIsImmutableAcrossCalls 每次调用返回独立切片，避免跨调用共享导致测试/调用方相互污染。
func TestDefaultConfigIsImmutableAcrossCalls(t *testing.T) {
	a := DefaultGlobalConfig()
	b := DefaultGlobalConfig()
	a.DNS.DOHServers[0] = "mutated"
	if b.DNS.DOHServers[0] != "https://cloudflare-dns.com/dns-query" {
		t.Errorf("两次调用共享了 DOHServers 切片: b[0] = %q", b.DNS.DOHServers[0])
	}
}

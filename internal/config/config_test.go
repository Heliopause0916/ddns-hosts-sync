package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// baseConfig 返回一份通过校验的基础全局配置，便于按用例单字段破坏。
func baseGlobal() *model.GlobalConfig {
	g := model.DefaultGlobalConfig()
	return &g
}

func baseEntry(id, source, target string) model.Entry {
	return model.Entry{ID: id, Source: source, Target: target, Enabled: true}
}

// ---------------------------------------------------------------------------
// 规则 1：interval 上下界
// ---------------------------------------------------------------------------

func TestValidateInterval(t *testing.T) {
	cases := []struct {
		interval int
		wantErr  bool
	}{
		{1, false}, {1440, false}, {5, false},
		{0, true}, {-1, true}, {1441, true}, {99999, true},
	}
	for _, tc := range cases {
		g := baseGlobal()
		g.IntervalMinutes = tc.interval
		err := ValidateGlobal(g)
		if (err != nil) != tc.wantErr {
			t.Errorf("interval=%d: got err=%v, wantErr=%v", tc.interval, err, tc.wantErr)
		}
	}
}

// ---------------------------------------------------------------------------
// 规则 2/3：source 与 target 合法/非法（含 IP 字面量）
// ---------------------------------------------------------------------------

func TestValidateEntrySource(t *testing.T) {
	valid := []string{
		"entry.example.com",
		"a.b.c.example.com",
		"my-host-1.example.net",
		"xn--bcher-kva.example",
	}
	for _, s := range valid {
		if err := ValidateEntry(baseEntry("e1", s, "target.example.com")); err != nil {
			t.Errorf("source=%q 应为合法 FQDN, got %v", s, err)
		}
	}
	invalid := []string{
		"", "singlelabel", "with_underscore.example.com", "has space.example.com",
		"-leading.example.com", "trailing-.example.com", ".example.com",
		"example..com", "example.com.", "exa mple.com", strings.Repeat("a.", 130) + "com",
		"a@b.com", "a/b.com", // 含特殊字符
		// 注：规则 2 仅禁 FQDN 语法非法；IP 字面量禁止是规则 3 的 target 专属，
		// source 的 IP 形式在此视为语法合法（解析阶段再处置）。
	}
	for _, s := range invalid {
		if err := ValidateEntry(baseEntry("e1", s, "target.example.com")); err == nil {
			t.Errorf("source=%q 应为非法，但通过校验", s)
		}
	}
}

func TestValidateEntryTarget(t *testing.T) {
	valid := []string{
		"target.example.com", "nas.home", "rd.server.com", "a.b.c",
	}
	for _, s := range valid {
		if err := ValidateEntry(baseEntry("e1", "src.example.com", s)); err != nil {
			t.Errorf("target=%q 应为合法 FQDN, got %v", s, err)
		}
	}
	invalid := []string{
		"", "singlelabel", "with_underscore.example.com", "bad target.example.com",
		"192.168.1.1", "10.0.0.1", "2001:db8::1", "::1",
		"0x7f000001", "0177.0.0.1", // IPv4 变体字面量
	}
	for _, s := range invalid {
		if err := ValidateEntry(baseEntry("e1", "src.example.com", s)); err == nil {
			t.Errorf("target=%q 应为非法（FQDN 或 IP 字面量），但通过校验", s)
		}
	}
}

// ---------------------------------------------------------------------------
// 规则 4：target 重复（仅 enabled 条目参与）
// ---------------------------------------------------------------------------

func TestValidateTargetDuplicate(t *testing.T) {
	dupCfg := func(secondEnabled bool) *Config {
		c := Default()
		c.Entries = []model.Entry{
			baseEntry("e1", "src1.example.com", "shared.example.com"),
			{ID: "e2", Source: "src2.example.com", Target: "shared.example.com", Enabled: secondEnabled},
		}
		return c
	}

	if err := dupCfg(true).Validate(); err == nil {
		t.Error("两个 enabled 条目 target 重复应校验失败")
	} else if !strings.Contains(err.Error(), "target 重复") {
		t.Errorf("错误信息未指明 target 重复: %v", err)
	}

	if err := dupCfg(false).Validate(); err != nil {
		t.Errorf("停用条目不参与 target 去重，应通过校验, got %v", err)
	}

	c := Default()
	c.Entries = []model.Entry{
		baseEntry("e1", "src1.example.com", "a.example.com"),
		baseEntry("e2", "src2.example.com", "b.example.com"),
	}
	if err := c.Validate(); err != nil {
		t.Errorf("target 互不相同应通过校验, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 规则 5：YAML 非法
// ---------------------------------------------------------------------------

func TestLoadInvalidYAML(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "config.yaml")

	if _, err := Load(path); err == nil {
		t.Error("文件不存在应返回错误")
	}

	bad := "version: [1, 2\n  enabled: true\nentries: {{{"
	if err := os.WriteFile(path, []byte(bad), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("非法 YAML 应返回解析错误")
	} else if !strings.Contains(err.Error(), "config 解析失败") {
		t.Errorf("错误应含解析失败上下文, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 规则 6/7：depth 与 timeout 边界
// ---------------------------------------------------------------------------

func TestValidateDepth(t *testing.T) {
	for _, d := range []int{1, 10, 30} {
		g := baseGlobal()
		g.MaxCNAMEDepth = d
		if err := ValidateGlobal(g); err != nil {
			t.Errorf("depth=%d 应在 [1,30] 内, got %v", d, err)
		}
	}
	for _, d := range []int{0, -5, 31, 100} {
		g := baseGlobal()
		g.MaxCNAMEDepth = d
		if err := ValidateGlobal(g); err == nil {
			t.Errorf("depth=%d 应越界, 但通过校验", d)
		}
	}
}

func TestValidateTimeout(t *testing.T) {
	for _, s := range []int{5, 15, 120} {
		g := baseGlobal()
		g.DNS.TimeoutSec = s
		if err := ValidateGlobal(g); err != nil {
			t.Errorf("timeout=%d 应在 [5,120] 内, got %v", s, err)
		}
	}
	for _, s := range []int{4, 0, 121, 1000} {
		g := baseGlobal()
		g.DNS.TimeoutSec = s
		if err := ValidateGlobal(g); err == nil {
			t.Errorf("timeout=%d 应越界, 但通过校验", s)
		}
	}
}

// ---------------------------------------------------------------------------
// 规则 8：ip_version 非法
// ---------------------------------------------------------------------------

func TestValidateIPVersion(t *testing.T) {
	for _, v := range []string{"ipv4", "ipv6", "both"} {
		g := baseGlobal()
		g.DNS.IPVersion = v
		if err := ValidateGlobal(g); err != nil {
			t.Errorf("ip_version=%q 应合法, got %v", v, err)
		}
	}
	for _, v := range []string{"", "bogus", "IPv4", "all"} {
		g := baseGlobal()
		g.DNS.IPVersion = v
		if err := ValidateGlobal(g); err == nil {
			t.Errorf("ip_version=%q 应非法, 但通过校验", v)
		}
	}
}

// ---------------------------------------------------------------------------
// 规则 9：doh_servers 空 / 非法
// ---------------------------------------------------------------------------

func TestValidateDOHServers(t *testing.T) {
	// 空
	g := baseGlobal()
	g.DNS.DOHServers = nil
	if err := ValidateGlobal(g); err == nil {
		t.Error("doh_servers 为空应校验失败")
	}
	// 混合合法 + 非法 → 严格校验失败
	g = baseGlobal()
	g.DNS.DOHServers = []string{"https://cloudflare-dns.com/dns-query", "not-a-url"}
	if err := ValidateGlobal(g); err == nil {
		t.Error("doh_servers 含非法 URL 应校验失败")
	}
	// 非 https 协议
	g = baseGlobal()
	g.DNS.DOHServers = []string{"http://cloudflare-dns.com/dns-query"}
	if err := ValidateGlobal(g); err == nil {
		t.Error("http 协议 URL 应校验失败")
	}
	// 全合法
	g = baseGlobal()
	g.DNS.DOHServers = []string{"https://cloudflare-dns.com/dns-query", "https://dns.google/resolve"}
	if err := ValidateGlobal(g); err != nil {
		t.Errorf("doh_servers 全合法应通过, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// 规则 10：note 截断（软限制，Normalize 执行）
// ---------------------------------------------------------------------------

func TestNormalizeEntryNoteTruncation(t *testing.T) {
	long := strings.Repeat("长", 250)
	e := model.Entry{Source: "SRC.Example.COM ", Target: " TARGET.Example.COM", Note: long}
	NormalizeEntry(&e)
	if got := len([]rune(e.Note)); got != noteMaxRunes {
		t.Errorf("note 应截断到 %d 字符, 实际 %d", noteMaxRunes, got)
	}
	if e.Source != "src.example.com" {
		t.Errorf("source 应小写去空白: %q", e.Source)
	}
	if e.Target != "target.example.com" {
		t.Errorf("target 应小写去空白: %q", e.Target)
	}

	// 恰好 200 字符不截断
	edge := strings.Repeat("a", noteMaxRunes)
	e2 := model.Entry{Source: "s.example.com", Target: "t.example.com", Note: edge}
	NormalizeEntry(&e2)
	if e2.Note != edge {
		t.Error("恰好 200 字符不应被截断")
	}
}

// TestValidateEntryNoteSoftLimit 校验规则 10 为软限制：超长 note 不被 Validate 拒绝。
func TestValidateEntryNoteSoftLimit(t *testing.T) {
	e := baseEntry("e1", "src.example.com", "target.example.com")
	e.Note = strings.Repeat("x", 500)
	if err := ValidateEntry(e); err != nil {
		t.Errorf("note 超长不应被严格校验拒绝（软限制）, got %v", err)
	}
}

// ---------------------------------------------------------------------------
// Normalize 夹取断言（规则 1/6/7/8/9 降级语义）
// ---------------------------------------------------------------------------

func TestNormalizeGlobalClamps(t *testing.T) {
	g := baseGlobal()
	g.IntervalMinutes, g.MaxCNAMEDepth, g.DNS.TimeoutSec = 0, 0, 1
	NormalizeGlobal(g)
	if g.IntervalMinutes != 1 || g.MaxCNAMEDepth != 1 || g.DNS.TimeoutSec != 5 {
		t.Errorf("下界夹取失败: interval=%d depth=%d timeout=%d, 期望 1/1/5",
			g.IntervalMinutes, g.MaxCNAMEDepth, g.DNS.TimeoutSec)
	}

	g = baseGlobal()
	g.IntervalMinutes, g.MaxCNAMEDepth, g.DNS.TimeoutSec = 1440*2, 99, 500
	NormalizeGlobal(g)
	if g.IntervalMinutes != 1440 || g.MaxCNAMEDepth != 30 || g.DNS.TimeoutSec != 120 {
		t.Errorf("上界夹取失败: interval=%d depth=%d timeout=%d, 期望 1440/30/120",
			g.IntervalMinutes, g.MaxCNAMEDepth, g.DNS.TimeoutSec)
	}
}

func TestNormalizeGlobalValueDegradation(t *testing.T) {
	g := baseGlobal()
	g.DNS.Mode = "ftp"
	g.DNS.IPVersion = "bogus"
	g.DNS.DOHServers = nil
	g.FailureKeepOld = false
	NormalizeGlobal(g)
	if g.DNS.Mode != "doh" {
		t.Errorf("非法 mode 应回落 doh, got %q", g.DNS.Mode)
	}
	if g.DNS.IPVersion != "ipv4" {
		t.Errorf("非法 ip_version 应回落 ipv4, got %q", g.DNS.IPVersion)
	}
	if len(g.DNS.DOHServers) != 2 {
		t.Errorf("空 doh_servers 应回落默认列表, got %v", g.DNS.DOHServers)
	}
	if !g.FailureKeepOld {
		t.Error("FailureKeepOld 应被强制为 true")
	}

	// 全非法 URL → 回落默认
	g = baseGlobal()
	g.DNS.DOHServers = []string{"a", "b", "ftp://x"}
	NormalizeGlobal(g)
	if len(g.DNS.DOHServers) != 2 {
		t.Errorf("全非法 doh_servers 应回落默认, got %v", g.DNS.DOHServers)
	}

	// 混合 → 仅保留合法
	g = baseGlobal()
	g.DNS.DOHServers = []string{"https://cloudflare-dns.com/dns-query", "junk"}
	NormalizeGlobal(g)
	if len(g.DNS.DOHServers) != 1 || g.DNS.DOHServers[0] != "https://cloudflare-dns.com/dns-query" {
		t.Errorf("混合列表应只保留合法 URL, got %v", g.DNS.DOHServers)
	}
}

func TestNormalizeVersionFallback(t *testing.T) {
	c := Default()
	c.Version = 99
	c.IntervalMinutes = 7
	c.Entries = []model.Entry{baseEntry("e1", "s.example.com", "t.example.com")}
	c.Normalize()
	if c.Version != 1 || c.IntervalMinutes != 5 || len(c.Entries) != 0 {
		t.Errorf("version != 1 应整份回退默认（entries 清空）: version=%d interval=%d entries=%d",
			c.Version, c.IntervalMinutes, len(c.Entries))
	}
}

// ---------------------------------------------------------------------------
// 加载 / 保存 / 模板 往返
// ---------------------------------------------------------------------------

func TestSaveLoadRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	c := Default()
	c.Entries = []model.Entry{
		{ID: "e1", Source: "src.example.com", Target: "nas.home", Enabled: true, Note: "私有 NAS"},
		{ID: "e2", Source: "relay.example.org", Target: "relay.example.org", Enabled: false},
	}
	if err := c.Save(path); err != nil {
		t.Fatalf("Save: %v", err)
	}
	got, err := Load(path)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got.IntervalMinutes != c.IntervalMinutes || got.DNS.Mode != c.DNS.Mode ||
		got.Version != model.ConfigVersion {
		t.Errorf("全局字段往返不一致: %+v", got.GlobalConfig)
	}
	if len(got.Entries) != 2 || got.Entries[0].ID != "e1" || !got.Entries[0].Enabled {
		t.Errorf("entries 往返不一致: %+v", got.Entries)
	}
	if got.Entries[1].Enabled {
		t.Error("enabled=false 往返后应为 false")
	}
}

func TestDefaultTemplate(t *testing.T) {
	data, err := DefaultTemplate()
	if err != nil {
		t.Fatalf("DefaultTemplate: %v", err)
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		t.Fatalf("模板应可直接解析: %v", err)
	}
	if err := c.Validate(); err != nil {
		t.Errorf("默认模板应通过严格校验, got %v", err)
	}
	if len(c.Entries) != 0 {
		t.Errorf("默认模板 entries 应为空, got %d", len(c.Entries))
	}
}

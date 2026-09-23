// Package config 提供 config.yaml 的加载/保存/默认模板，以及 DSD §1.1 校验规则表
// 1-10 的判定逻辑（Normalize 夹取 + Validate 严格校验）。
//
// GUI 与任务端共用同一套校验：GUI 保存走 Validate（严格、返回首条错误），任务端
// 读入走 Normalize（夹取降级）。YAML 解析失败的兜底（回退 DefaultGlobalConfig）
// 由调用方按 DSD 规则表第 5 条执行，本包 Load 仅返回解析错误。
package config

import (
	"bytes"
	"fmt"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"gopkg.in/yaml.v3"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// Config 是 config.yaml 的顶层结构：GlobalConfig 字段内联展开为顶层键，
// entries 为条目列表（DSD §1.1 + §1.2）。
type Config struct {
	model.GlobalConfig `yaml:",inline"`
	Entries            []model.Entry `yaml:"entries"`
}

// Default 返回默认配置（含空条目列表）。
func Default() *Config {
	return &Config{GlobalConfig: model.DefaultGlobalConfig()}
}

// ---------------------------------------------------------------------------
// 校验规则阈值 / 值域（DSD §1.1 校验规则表 1-10）
// ---------------------------------------------------------------------------

const (
	minInterval, maxInterval = 1, 1440 // 规则 1
	minDepth, maxDepth       = 1, 30   // 规则 6
	minTimeout, maxTimeout   = 5, 120  // 规则 7
	noteMaxRunes             = 200     // 规则 10（软限制：截断）
	modeDOH, modeSystem      = "doh", "system"
	ipv4, ipv6, both         = "ipv4", "ipv6", "both"
	maxFQDNLen               = 253
)

var (
	// fqdnLabel 单标签：字母数字开头结尾，中部可含连字符，长度 ≤63。
	fqdnLabel = regexp.MustCompile(`^[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)

	// templateHeader 生成配置模板的说明头。
	templateHeader = `# ddns-hosts-sync 默认配置文件
# 由 install 生成；修改后由下一个同步周期（≤1 分钟）生效。
#
`
)

// ---------------------------------------------------------------------------
// 加载 / 保存 / 模板
// ---------------------------------------------------------------------------

// Load 读取并解析 config.yaml。YAML 非法返回错误（规则 5，调用方按角色降级）。
// 文件不存在同样返回错误。
func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var c Config
	if err := yaml.Unmarshal(data, &c); err != nil {
		return nil, fmt.Errorf("config 解析失败: %w", err)
	}
	return &c, nil
}

// Save 将配置序列化为 YAML 并以原子写（同目录 temp + fsync + rename）落盘，
// 避免任务端读到半截文件。
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return err
	}
	return atomicWriteFile(path, data, 0o644)
}

// DefaultTemplate 生成默认配置模板内容（供 install 预置 config.yaml）。
// 返回 YAML 字节流，可直接解析回 *Config 且通过 Validate。
func DefaultTemplate() ([]byte, error) {
	var buf bytes.Buffer
	buf.WriteString(templateHeader)
	data, err := yaml.Marshal(Default())
	if err != nil {
		return nil, err
	}
	buf.Write(data)
	return buf.Bytes(), nil
}

// ---------------------------------------------------------------------------
// Normalize：任务端降级语义（夹取非法值）
// ---------------------------------------------------------------------------

// Normalize 对完整配置执行降级归一：
//   - version != 1 时整份回退默认（DSD §1.1，entries 清空）；
//   - 全局字段按规则 1/6/7/8/9 夹取（NormalizeGlobal）；
//   - 条目 source/target 小写、note 按规则 10 截断（NormalizeEntry）。
//
// M0 未含 logging 包，夹取不发 warning 日志，由 M1+ 接入。
func (c *Config) Normalize() {
	if c.Version != model.ConfigVersion {
		*c = Config{GlobalConfig: model.DefaultGlobalConfig()}
		return
	}
	NormalizeGlobal(&c.GlobalConfig)
	for i := range c.Entries {
		NormalizeEntry(&c.Entries[i])
	}
}

// NormalizeGlobal 夹取全局字段非法值（规则 1/6/7/8/9 + mode/failure_keep_old）。
func NormalizeGlobal(g *model.GlobalConfig) {
	if g.Version != model.ConfigVersion {
		*g = model.DefaultGlobalConfig()
		return
	}
	if g.IntervalMinutes < minInterval {
		g.IntervalMinutes = minInterval
	}
	if g.IntervalMinutes > maxInterval {
		g.IntervalMinutes = maxInterval
	}
	if g.MaxCNAMEDepth < minDepth {
		g.MaxCNAMEDepth = minDepth
	}
	if g.MaxCNAMEDepth > maxDepth {
		g.MaxCNAMEDepth = maxDepth
	}
	if g.DNS.TimeoutSec < minTimeout {
		g.DNS.TimeoutSec = minTimeout
	}
	if g.DNS.TimeoutSec > maxTimeout {
		g.DNS.TimeoutSec = maxTimeout
	}
	if g.DNS.Mode != modeDOH && g.DNS.Mode != modeSystem {
		g.DNS.Mode = modeDOH
	}
	switch g.DNS.IPVersion {
	case ipv4, ipv6, both:
		// 合法，保持
	default:
		g.DNS.IPVersion = ipv4 // 规则 8：非法值回落 ipv4
	}
	g.DNS.DOHServers = normalizeDOHServers(g.DNS.DOHServers) // 规则 9：空/全非法回落默认
	g.FailureKeepOld = true                                  // v1 锁定
}

// NormalizeEntry 归一化单条目：source/target 小写去空白，note 截断至 200 字符（规则 10）。
func NormalizeEntry(e *model.Entry) {
	e.Source = strings.ToLower(strings.TrimSpace(e.Source))
	e.Target = strings.ToLower(strings.TrimSpace(e.Target))
	if r := []rune(e.Note); len(r) > noteMaxRunes {
		e.Note = string(r[:noteMaxRunes])
	}
}

// normalizeDOHServers 过滤出合法 https URL；结果为空时回落默认列表（规则 9）。
func normalizeDOHServers(servers []string) []string {
	out := make([]string, 0, len(servers))
	for _, s := range servers {
		s = strings.TrimSpace(s)
		if isValidHTTPSURL(s) {
			out = append(out, s)
		}
	}
	if len(out) == 0 {
		return model.DefaultGlobalConfig().DNS.DOHServers
	}
	return out
}

// ---------------------------------------------------------------------------
// Validate：严格校验（GUI 保存用），返回首条错误
// ---------------------------------------------------------------------------

// Validate 完整校验（全局 + 条目），命中首条错误即返回（DSD：full 校验返回首条错误）。
func (c *Config) Validate() error {
	if err := ValidateGlobal(&c.GlobalConfig); err != nil {
		return err
	}
	return validateEntries(c.Entries)
}

// ValidateGlobal 严格校验全局字段：规则 1/6/7/8/9 + version/mode/failure_keep_old。
func ValidateGlobal(g *model.GlobalConfig) error {
	if g.Version != model.ConfigVersion {
		return fmt.Errorf("config.version 必须为 %d，实际 %d", model.ConfigVersion, g.Version)
	}
	if g.IntervalMinutes < minInterval || g.IntervalMinutes > maxInterval { // 规则 1
		return fmt.Errorf("interval_minutes 必须在 [%d, %d] 范围内，实际 %d", minInterval, maxInterval, g.IntervalMinutes)
	}
	if g.MaxCNAMEDepth < minDepth || g.MaxCNAMEDepth > maxDepth { // 规则 6
		return fmt.Errorf("max_cname_depth 必须在 [%d, %d] 范围内，实际 %d", minDepth, maxDepth, g.MaxCNAMEDepth)
	}
	if g.DNS.TimeoutSec < minTimeout || g.DNS.TimeoutSec > maxTimeout { // 规则 7
		return fmt.Errorf("dns.timeout_sec 必须在 [%d, %d] 范围内，实际 %d", minTimeout, maxTimeout, g.DNS.TimeoutSec)
	}
	if g.DNS.Mode != modeDOH && g.DNS.Mode != modeSystem {
		return fmt.Errorf("dns.mode 必须是 %q 或 %q，实际 %q", modeDOH, modeSystem, g.DNS.Mode)
	}
	switch g.DNS.IPVersion { // 规则 8
	case ipv4, ipv6, both:
	default:
		return fmt.Errorf("dns.ip_version 必须是 ipv4/ipv6/both 之一，实际 %q", g.DNS.IPVersion)
	}
	if len(g.DNS.DOHServers) == 0 { // 规则 9
		return fmt.Errorf("dns.doh_servers 不能为空")
	}
	for i, s := range g.DNS.DOHServers {
		if !isValidHTTPSURL(s) {
			return fmt.Errorf("dns.doh_servers[%d] 必须是 https URL，实际 %q", i, s)
		}
	}
	if !g.FailureKeepOld {
		return fmt.Errorf("failure_keep_old 恒为 true（v1 锁定）")
	}
	return nil
}

// ValidateEntry 严格校验单条目：规则 2（source FQDN）、规则 3（target FQDN 且非
// IP 字面量）。规则 10（note）为软限制不在此拒绝——由 NormalizeEntry 截断。
func ValidateEntry(e model.Entry) error {
	if !isValidFQDN(e.Source) { // 规则 2
		return fmt.Errorf("source 非法 FQDN: %q", e.Source)
	}
	if !isValidFQDN(e.Target) { // 规则 3
		return fmt.Errorf("target 非法 FQDN: %q", e.Target)
	}
	if isIPLiteral(e.Target) { // 规则 3：不得为 IP 字面量
		return fmt.Errorf("target 不得为 IP 字面量: %q", e.Target)
	}
	return nil
}

// validateEntries 批量校验条目：逐条规则 2/3，随后规则 4（仅 enabled 条目的
// target 全局唯一；冲突保留序首个）。
func validateEntries(entries []model.Entry) error {
	for i := range entries {
		if err := ValidateEntry(entries[i]); err != nil {
			return fmt.Errorf("entries[%d] (id=%s): %w", i, entries[i].ID, err)
		}
	}
	seen := make(map[string]string, len(entries))
	for i := range entries {
		if !entries[i].Enabled {
			continue
		}
		t := strings.ToLower(strings.TrimSpace(entries[i].Target))
		if firstID, ok := seen[t]; ok { // 规则 4
			return fmt.Errorf("target 重复: %q 同时被条目 %s 与 %s 使用", t, firstID, entries[i].ID)
		}
		seen[t] = entries[i].ID
	}
	return nil
}

// ---------------------------------------------------------------------------
// 判定助手
// ---------------------------------------------------------------------------

// isValidFQDN 判定合法 FQDN：多标签（≥2）、标签含连字符但不得起止连字符、
// 长度 ≤253，不含下划线/空白/端口等（DSD §1.2）。不允许尾点。
func isValidFQDN(s string) bool {
	if s == "" || len(s) > maxFQDNLen {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !fqdnLabel.MatchString(l) {
			return false
		}
	}
	return true
}

// isIPLiteral 检测字符串是否为 IP 字面量：net.ParseIP 覆盖标准 IPv4/IPv6，
// 另补 inet_aton 风格的四段纯数字形式（可带前导零，如 "0177.0.0.1"，
// net.ParseIP 不识别）。hosts 文件首列必须是合法 IP，落到 target 上这类
// 全数字四段串在配置语境中只可能是 IP 地址。
func isIPLiteral(s string) bool {
	s = strings.TrimSpace(s)
	if s == "" {
		return false
	}
	if net.ParseIP(s) != nil {
		return true
	}
	parts := strings.Split(s, ".")
	if len(parts) != 4 {
		return false
	}
	for _, p := range parts {
		if p == "" {
			return false
		}
		for _, c := range p {
			if c < '0' || c > '9' {
				return false
			}
		}
	}
	return true
}

// isValidHTTPSURL 判定 https URL（规则 9）。
func isValidHTTPSURL(s string) bool {
	u, err := url.Parse(s)
	if err != nil {
		return false
	}
	return u.Scheme == "https" && u.Host != ""
}

// ---------------------------------------------------------------------------
// 原子写（config 自持，避免与 state 包耦合）
// ---------------------------------------------------------------------------

// atomicWriteFile 以 `<name>.<pid>.<nanotime>.tmp` 写入同目录 → fsync → rename 覆盖。
func atomicWriteFile(path string, data []byte, perm os.FileMode) error {
	tmp := filepath.Join(filepath.Dir(path),
		fmt.Sprintf("%s.%d.%d.tmp", filepath.Base(path), os.Getpid(), time.Now().UnixNano()))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		_ = os.Remove(tmp)
		if werr != nil {
			return werr
		}
		if serr != nil {
			return serr
		}
		return cerr
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	return nil
}

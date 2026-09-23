// Package model 定义 ddns-hosts-sync 核心数据结构（DSD §1）。
//
// YAML 与 JSON 序列化标签一致（字段名 snake_case）：YAML 用于 config.yaml，
// JSON 用于 state/status.json 与 state/trigger.json 进程间通信文件。
package model

// GlobalConfig 全局配置（DSD §1.1）。config.yaml 顶层字段，entries 列表为同级
// 集合（见 internal/config 的 Config 包装）。
//
// 校验与降级语义见 DSD §1.1 校验规则表 1-10；实现位于 internal/config
// （Validate/Normalize），本项目保持判定逻辑单源、GUI 与任务共用。
type GlobalConfig struct {
	Version         int       `yaml:"version"`          // 配置 schema 版本，恒为 1
	Enabled         bool      `yaml:"enabled"`          // 暂停自动同步开关（默认 true）
	IntervalMinutes int       `yaml:"interval_minutes"` // [1,1440]，默认 5；任务注册粒度固定 1 分钟，此值仅做进程内到点判断
	DNS             DNSConfig `yaml:"dns"`
	MaxCNAMEDepth   int       `yaml:"max_cname_depth"`  // [1,30]，默认 10
	FailureKeepOld  bool      `yaml:"failure_keep_old"` // 恒 true（v1 锁定，字段保留）
	FlushDNS        bool      `yaml:"flush_dns"`        // 写盘成功后执行系统 DNS 缓存刷新（默认 true）
}

// DNSConfig DNS 解析通道配置（DSD §1.1）。
type DNSConfig struct {
	Mode       string   `yaml:"mode"`        // "doh" | "system"，默认 doh
	DOHServers []string `yaml:"doh_servers"` // 默认 [cloudflare, dns.google]；依次尝试，全部失败回落系统解析
	TimeoutSec int      `yaml:"timeout_sec"` // [5,120]，默认 15；单条目全链路超时
	IPVersion  string   `yaml:"ip_version"`  // "ipv4" | "ipv6" | "both"，默认 ipv4
}

// DefaultGlobalConfig 返回 DSD §1.1 规定的默认全局配置。
func DefaultGlobalConfig() GlobalConfig {
	return GlobalConfig{
		Version:         1,
		Enabled:         true,
		IntervalMinutes: 5,
		DNS: DNSConfig{
			Mode:       "doh",
			DOHServers: []string{"https://cloudflare-dns.com/dns-query", "https://dns.google/resolve"},
			TimeoutSec: 15,
			IPVersion:  "ipv4",
		},
		MaxCNAMEDepth:  10,
		FailureKeepOld: true,
		FlushDNS:       true,
	}
}

// 协议版本常量（DSD §5.1：status/trigger version=1，config version=1）。
const (
	// ConfigVersion 当前配置 schema 版本。
	ConfigVersion = 1
	// StatusVersion 当前 status/trigger 协议版本。
	StatusVersion = 1
)

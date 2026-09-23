package model

// Entry 单条 "CNAME 到 hosts" 映射（DSD §1.2）。
type Entry struct {
	ID      string `yaml:"id"`      // 创建时生成 8 字符 UUID 短码；全局唯一、终身不变
	Source  string `yaml:"source"`  // 合法 FQDN（多标签、连字符；不得含 _ 空格）；保存时 lower
	Target  string `yaml:"target"`  // 合法 FQDN 且不得为 IP 字面量；保存时 lower；可与 source 相同
	Enabled bool   `yaml:"enabled"` // 停用条目：不解析、不写、不删除 hosts 已有行
	Note    string `yaml:"note"`    // ≤200 字符，仅 GUI 展示
}

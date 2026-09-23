package model

import "time"

// SyncStatus 状态协议顶层结构（state/status.json，DSD §1.4）。
// 唯一写者 = 任务；GUI 轮询读。
type SyncStatus struct {
	Version         int              `json:"version"`
	UpdatedAt       time.Time        `json:"updated_at"`
	NextScheduledAt time.Time        `json:"next_scheduled_at"`
	IntervalMinutes int              `json:"interval_minutes"`
	SyncWindow      SyncWindowState  `json:"sync_window"`
	Task            TaskStatus       `json:"task"`
	Entries         []EntryStatus    `json:"entries"`
	HostsBlock      HostsBlockStatus `json:"hosts_block"`
}

// TaskStatus 最近一次任务运行摘要（DSD §1.4）。
type TaskStatus struct {
	LastRunAt time.Time `json:"last_run_at"`
	LastRunOK bool      `json:"last_run_ok"`
	LastError string    `json:"last_error"`
}

// SyncWindowState 本轮窗口状态枚举（DSD §1.4）。
type SyncWindowState string

const (
	// WindowWaiting 本轮醒来未到间隔，未干活。
	WindowWaiting SyncWindowState = "waiting"
	// WindowSynced 本轮执行了完整同步。
	WindowSynced SyncWindowState = "synced"
)

// EntryStatus 单条目同步状态（DSD §1.5）。
type EntryStatus struct {
	ID                 string     `json:"id"`
	Enabled            bool       `json:"enabled"`
	Status             EntryState `json:"status"`
	ResolvedIPs        []string   `json:"resolved_ips"`
	ResolvedCNAMEChain []string   `json:"resolved_cname_chain"`
	ResolvedAt         time.Time  `json:"resolved_at"`
	UsedFallback       bool       `json:"used_fallback"`
	HostsPresent       bool       `json:"hosts_present"`
	Error              string     `json:"error"`
}

// EntryState 条目状态枚举全集（DSD §1.5，共 8 值）。
type EntryState string

const (
	// EntryOK 解析成功且需写内容已写入 hosts（或与现有块一致）。
	EntryOK EntryState = "ok"
	// EntryNXDomain 解析链上出现 NXDOMAIN。
	EntryNXDomain EntryState = "nxdomain"
	// EntryCNAMELoop 检测到 CNAME 环（error 含环上节点）。
	EntryCNAMELoop EntryState = "cname_loop"
	// EntryTooDeep 超过 max_cname_depth。
	EntryTooDeep EntryState = "too_deep"
	// EntryTimeout 所有解析通道失败/超时。
	EntryTimeout EntryState = "timeout"
	// EntryWriteFailed 解析成功但 hosts 写盘失败。
	EntryWriteFailed EntryState = "write_failed"
	// EntryPaused enabled=false 被跳过。
	EntryPaused EntryState = "paused"
	// EntryInvalidConfig source/target 非法或 target 冲突（任务端手工冲突兜底）。
	EntryInvalidConfig EntryState = "invalid_config"
)

// HostsBlockStatus hosts 标记块状态（DSD §1.6）。
type HostsBlockStatus struct {
	Present     bool      `json:"present"`      // 块是否存在于 hosts（markers 是否完整成对）
	ContentMD5  string    `json:"content_md5"`  // 当前 hosts 文件中块的实际 md5
	ExpectedMD5 string    `json:"expected_md5"` // 最近一次期望块文本 md5
	LastWriteAt time.Time `json:"last_write_at"`
	LastWriteOK bool      `json:"last_write_ok"`
}

package model

import "time"

// Trigger 触发协议文件（state/trigger.json，DSD §1.3 / §5.1）。
// 唯一写者 = GUI；唯一消费者 = 任务；GUI 永不读 trigger。
type Trigger struct {
	Version     int           `json:"version"`
	RequestID   string        `json:"request_id"`   // UUID 串：幂等去重/消费后归档命名
	RequestedAt time.Time     `json:"requested_at"` // RFC3339 UTC：任务侧新鲜度判断基准（≤2 分钟视为有效）
	Action      TriggerAction `json:"action"`       // 消费分支
}

// TriggerAction 触发动作枚举。
type TriggerAction string

const (
	// ActionSyncNow GUI "立即同步"。
	ActionSyncNow TriggerAction = "sync_now"
	// ActionReconfigure 预留：配置类重注册请求（v1 无消费方，仅记日志）。
	ActionReconfigure TriggerAction = "reconfigure"
)

// Package tray 实现托盘常驻生命周期（DSD §4.3 四色状态机、§7.9 单实例锁、
// 右键菜单、tooltip），fyne.io/systray 独立管理图标与菜单循环，fyne 仅由
// 调用方（cmd）用于渲染配置窗口（DSD §7.1：不调用 fyne 自带 dock/tray）。
//
// 导入方向硬约束：本包不 import internal/platform、internal/hostsfile、
// internal/resolver；路径与动作全部经 Tray.Deps 注入。
//
// 可测性：状态判定、tooltip、菜单规格、单实例锁均为纯函数/独立组件；
// systray 系统托盘循环无法在 headless 下启动，命令面收敛于 traySvc 接口
// （见 tray.go），单测注入 fake 断言刷新逻辑。
package tray

import (
	"strings"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// ---------------------------------------------------------------------------
// 托盘四色状态（DSD §4.3）
// ---------------------------------------------------------------------------

// Color 托盘图标状态色。
type Color int

const (
	// ColorGray 后台同步未运行（status 缺失/陈旧）。
	ColorGray Color = iota
	// ColorGreen 最近一次同步全部正常。
	ColorGreen
	// ColorYellow 存在条目级错误或 warn 级事项，hosts 完好。
	ColorYellow
	// ColorRed 写盘失败或 hosts 块丢失/被篡改（最高优先级）。
	ColorRed
)

// IconName 映射 assets 图标文件名（与 assets.Icon{Color} 常量对应）。
func (c Color) IconName() string {
	switch c {
	case ColorGreen:
		return "green"
	case ColorYellow:
		return "yellow"
	case ColorRed:
		return "red"
	default:
		return "gray"
	}
}

// Label 状态中文标签（tooltip/面板文案）。
func (c Color) Label() string {
	switch c {
	case ColorGreen:
		return "同步正常"
	case ColorYellow:
		return "存在告警"
	case ColorRed:
		return "同步失败"
	default:
		return "后台同步未运行"
	}
}

// ---------------------------------------------------------------------------
// 陈旧阈值与状态判定（纯函数）
// ---------------------------------------------------------------------------

// StaleConfigAge 陈旧判定封顶（DSD §7.10 与 §4.3）：max(2×interval, 6h)，
// 防止用户配置 1440 分钟间隔后误报灰。
const StaleConfigAge = 6 * time.Hour

// StaleThreshold 返回 status 判定"陈旧"的时限。interval 下限夹取 1，
// 防御任务端 Normalize 之前的异常值（interval=0 视为 1）。
func StaleThreshold(intervalMinutes int) time.Duration {
	if intervalMinutes < 1 {
		intervalMinutes = 1
	}
	iv := time.Duration(intervalMinutes) * time.Minute
	twice := 2 * iv
	if twice > StaleConfigAge {
		return StaleConfigAge
	}
	return twice
}

// Stale 判定 status 是否已陈旧（含缺失/零值时间戳）。
func Stale(s *model.SyncStatus, now time.Time) bool {
	if s == nil || s.UpdatedAt.IsZero() {
		return true
	}
	return now.Sub(s.UpdatedAt) > StaleThreshold(s.IntervalMinutes)
}

// redSetEntry 红条件补充：条目状态 write_failed 直接判红（DSD §3.2 写失败行）。
func hasWriteFailedEntry(statuses []model.EntryStatus) bool {
	for i := range statuses {
		if statuses[i].Status == model.EntryWriteFailed {
			return true
		}
	}
	return false
}

// yellowSet 黄条件条目状态集合（DSD §4.3 黄条件 1）；
// paused/ok 不引起黄，write_failed 已在红通道处理。
func isYellowEntry(st model.EntryState) bool {
	switch st {
	case model.EntryNXDomain, model.EntryCNAMELoop, model.EntryTooDeep,
		model.EntryTimeout, model.EntryInvalidConfig:
		return true
	default:
		return false
	}
}

// hasYellowEntry 是否存在黄条件条目。
func hasYellowEntry(statuses []model.EntryStatus) bool {
	for i := range statuses {
		if isYellowEntry(statuses[i].Status) {
			return true
		}
	}
	return false
}

// warnish 黄条件 2：last_error 以 "warn:" 开头（备份/flushdns 失败，§3.2），
// 或含"已自动重写"（块缺失/篡改自愈警告，§3.2 篡改行：本轮有 warn → 黄）。
func warnish(lastError string) bool {
	s := strings.TrimSpace(lastError)
	if s == "" {
		return false
	}
	if strings.HasPrefix(s, "warn:") {
		return true
	}
	return strings.Contains(s, "已自动重写")
}

// DecideColor 依据 DSD §4.3 优先级裁定托盘颜色：灰独立通道（缺失/陈旧），
// 其余红 > 黄 > 绿。纯函数，便于单测全组合。
func DecideColor(s *model.SyncStatus, now time.Time) Color {
	if Stale(s, now) {
		return ColorGray
	}
	// 红：最高优先级，覆盖一切。
	if !s.Task.LastRunOK && strings.Contains(s.Task.LastError, "写盘失败") {
		return ColorRed
	}
	if !s.HostsBlock.LastWriteOK {
		return ColorRed
	}
	if !s.HostsBlock.Present && len(s.Entries) > 0 {
		return ColorRed
	}
	if hasWriteFailedEntry(s.Entries) {
		return ColorRed
	}
	// 黄。
	if hasYellowEntry(s.Entries) {
		return ColorYellow
	}
	if warnish(s.Task.LastError) {
		return ColorYellow
	}
	return ColorGreen
}

// ---------------------------------------------------------------------------
// tooltip 文案（DSD §4.3：恒显示最近同步时间 + 首条告警摘要）
// ---------------------------------------------------------------------------

// FirstAlert 提取首条告警摘要：优先非 ok/paused 条目的 error，其次
// task.last_error（剥掉 "warn: " 前缀）。无告警返回 ("", false)。
func FirstAlert(s *model.SyncStatus) (string, bool) {
	if s == nil {
		return "", false
	}
	if s.Task.LastError != "" {
		msg := strings.TrimSpace(s.Task.LastError)
		if msg != "" {
			const warnPrefix = "warn: "
			if strings.HasPrefix(msg, warnPrefix) {
				msg = strings.TrimSpace(strings.TrimPrefix(msg, warnPrefix))
			}
			return msg, true
		}
	}
	for i := range s.Entries {
		e := &s.Entries[i]
		if e.Status == model.EntryOK || e.Status == model.EntryPaused {
			continue
		}
		if strings.TrimSpace(e.Error) != "" {
			return strings.TrimSpace(e.Error), true
		}
	}
	return "", false
}

// Tooltip 组装托盘悬浮文案：
//   - 灰：固定提示"后台同步未运行，请检查计划任务"；
//   - 非灰："最近同步 <UTC+8 时间> ｜ 告警: <摘要>"（无告警仅时间）。
func Tooltip(s *model.SyncStatus, color Color) string {
	if s == nil || color == ColorGray || s.UpdatedAt.IsZero() {
		return "后台同步未运行，请检查计划任务"
	}
	base := "最近同步 " + s.UpdatedAt.Local().Format("2006-01-02 15:04:05")
	if alert, ok := FirstAlert(s); ok {
		return base + " ｜ 告警: " + alert
	}
	return base
}

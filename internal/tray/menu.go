package tray

// ---------------------------------------------------------------------------
// 托盘右键菜单规格（纯函数，单测可断言；DSD §4 与 ARCH §3.2）。
// 菜单项：打开配置 / 立即同步 / 查看日志目录 / 暂停自动同步(勾选开关) / 退出。
// ---------------------------------------------------------------------------

// 菜单项标识（事件分发与测试断言的稳定 ID）。
const (
	MenuOpenConfig     = "open_config"
	MenuSyncNow        = "sync_now"
	MenuViewLogs       = "view_logs"
	MenuPauseToggle    = "pause_toggle"
	MenuQuit           = "quit"
	MenuSepBeforePause = "sep_pause"
	MenuSepBeforeQuit  = "sep_quit"
)

// MenuSpec 单个菜单项的可测试规格。
type MenuSpec struct {
	ID        string // 稳定标识
	Label     string // 展示文案
	Checkable bool   // 是否为复选开关项（暂停同步）
	Checked   bool   // 复选项的当前勾选态
	Separator bool   // 是否仅分隔符
}

// separator 便捷构造。
func separator(id string) MenuSpec {
	return MenuSpec{ID: id, Separator: true}
}

// BuildMenuSpec 依据当前"暂停自动同步"状态构建完整菜单规格（确定性顺序）。
// paused=true 表示暂停中（勾选打上）。
func BuildMenuSpec(paused bool) []MenuSpec {
	return []MenuSpec{
		{ID: MenuOpenConfig, Label: "打开配置"},
		{ID: MenuSyncNow, Label: "立即同步"},
		{ID: MenuViewLogs, Label: "查看日志目录"},
		separator(MenuSepBeforePause),
		{ID: MenuPauseToggle, Label: "暂停自动同步", Checkable: true, Checked: paused},
		separator(MenuSepBeforeQuit),
		{ID: MenuQuit, Label: "退出"},
	}
}

// menuLabel 表驱动文案（供测试与日志引用；MenuSpec 已含 Label，此为文档性映射）。
var menuLabel = map[string]string{
	MenuOpenConfig:  "打开配置",
	MenuSyncNow:     "立即同步",
	MenuViewLogs:    "查看日志目录",
	MenuPauseToggle: "暂停自动同步",
	MenuQuit:        "退出",
}

// Label 返回菜单项文案（未知 ID 返回原样）。
func Label(id string) string {
	if s, ok := menuLabel[id]; ok {
		return s
	}
	return id
}

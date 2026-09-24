package tray

import (
	"fyne.io/systray"

	"github.com/heliopause/ddns-hosts-sync/assets"
)

// ---------------------------------------------------------------------------
// fyne.io/systray 命令面适配（薄层，唯一 import systray 的文件）。
//
// 选型说明（DSD §7.1）：独立库管理图标与菜单循环、fyne 仅渲染配置窗口。
// 相比 fyne v2.8.1 原生 SetSystemTrayMenu/Icon/Window：
//   - 原生 API 在 Windows 驱动层不暴露 SetTooltip（无法满足 DSD §4.3
//     tooltip=最近同步+首条告警），独立库全平台实现 SetTooltip/SetTitle；
//   - 原生托盘将左键单击固定为 toggle、右键固定菜单，无定制余地；
//   - 版本即 fyne v2.8.1 依赖树锁定的同一模块，不引入第二份派生；
//   - 本程序不调用 app.SetSystemTrayMenu，fyne 驱动不会启动其内建 systray
//     循环，进程内仅此一个托盘循环（无焦点/退出历史问题）。
// ---------------------------------------------------------------------------

// systrayHost 将 fyne.io/systray 包级函数适配为 traySvc 接口（生产实现）。
type systrayHost struct{}

func newSystrayHost() traySvc { return &systrayHost{} }

func (systrayHost) Run(onReady, onExit func()) { systray.Run(onReady, onExit) }
func (systrayHost) Quit()                      { systray.Quit() }
func (systrayHost) ResetMenu()                 { systray.ResetMenu() }
func (systrayHost) AddSeparator()              { systray.AddSeparator() }
func (systrayHost) SetTitle(title string)      { systray.SetTitle(title) }
func (systrayHost) SetTooltip(text string)     { systray.SetTooltip(text) }
func (systrayHost) SetOnTapped(f func())       { systray.SetOnTapped(f) }
func (systrayHost) SetIcon(png []byte)         { systray.SetIcon(png) }

func (systrayHost) AddItem(title, tooltip string, checkable, checked bool) MenuHandle {
	var item *systray.MenuItem
	if checkable {
		item = systray.AddMenuItemCheckbox(title, tooltip, checked)
	} else {
		item = systray.AddMenuItem(title, tooltip)
	}
	return &systrayItem{item: item}
}

// systrayItem 适配 *systray.MenuItem 为 MenuHandle。
type systrayItem struct {
	item *systray.MenuItem
}

func (s *systrayItem) Clicked() <-chan struct{} { return s.item.ClickedCh }
func (s *systrayItem) Check(checked bool) {
	if checked {
		s.item.Check()
	} else {
		s.item.Uncheck()
	}
}

// trayIcon 从 assets 加载指定状态色的托盘图标（PNG 字节）。
func trayIcon(c Color) ([]byte, error) {
	return assets.Icon(c.IconName())
}

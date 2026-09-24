// Package assets 内嵌托盘四色状态图标与应用图标（DSD §3 目录结构 assets/）。
//
// M2b-2 交付占位图标（assets/icongen 程序生成）；M3 打磨正式图标时仅需替换
// icons/*.png 资源，本包 API 不变。所有图标以 //go:embed 内嵌，运行时无外部
// 文件依赖（进程以任意 CWD 启动均可加载）。
package assets

import (
	"embed"
	"fmt"
)

//go:embed icons/*.png
var iconsFS embed.FS

// 托盘四色状态图标名（与 internal/tray 状态机常量一一对应，经字符串解耦，
// 避免 assets 反向依赖 tray 造成 import 环）。
const (
	IconGray   = "gray"
	IconGreen  = "green"
	IconYellow = "yellow"
	IconRed    = "red"
)

// AllIconNames 全部图标名的确定性顺序（测试与初始化遍历用）。
func AllIconNames() []string {
	return []string{IconGray, IconGreen, IconYellow, IconRed}
}

// Icon 返回指定颜色的托盘图标 PNG 字节。未知颜色返回 nil 与错误
// （调用方自行回落默认图标，避免启动崩溃）。
func Icon(name string) ([]byte, error) {
	b, err := iconsFS.ReadFile("icons/" + name + ".png")
	if err != nil {
		return nil, fmt.Errorf("assets: 图标 %q 不可用: %w", name, err)
	}
	return b, nil
}

// AppIcon 应用图标（托盘默认态/配置窗口标题栏），复用绿色占位。
// 返回 (png, ok)：嵌入缺失时 ok=false，调用方忽略并继续。
func AppIcon() ([]byte, bool) {
	b, err := Icon(IconGreen)
	if err != nil {
		return nil, false
	}
	return b, true
}

// 命令 ddns-hosts-sync 是唯一分发点：按 argv 分派 tray|sync|install|uninstall|version
// （ARCHITECTURE.md §3.1，DSD §2.6 导入方向硬约束）。
//
// M0 实现 version 与子命令骨架；M1 实现 sync；M2b-1 实现 install/uninstall
// （tasks/platform 三平台）；M2b-2 实现 tray/gui（fyne 集成）；M2b-3 完成 tray
// 子命令接线（platform 路径注入 + gui 回调），M2 闭环。
package main

import (
	"fmt"
	"os"

	"github.com/heliopause/ddns-hosts-sync/internal/console"
)

// version 由 ldflags 注入：go build -ldflags "-X main.version=v0.1.0+<sha>"
// 未注入时输出 "dev"。
var version = "dev"

func main() {
	// 启动最先适配终端编码（GBK 等非 UTF-8 控制台按 UTF-8 渲染程序自身
	// 输出；非 Windows/非 tty 恒为 no-op，跨平台安全），随后才解析输出。
	console.WrapStdio()

	if len(os.Args) < 2 {
		usage()
		os.Exit(1)
	}

	switch os.Args[1] {
	case "version":
		fmt.Println(version)
	case "sync":
		os.Exit(runSync(os.Args[2:]))
	case "install":
		os.Exit(runInstall(os.Args[2:]))
	case "uninstall":
		os.Exit(runUninstall(os.Args[2:]))
	case "tray":
		os.Exit(runTray(os.Args[2:]))
	default:
		usage()
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `ddns-hosts-sync —— 通用 CNAME→hosts 同步工具

用法:
  ddns-hosts-sync <子命令>

子命令:
  tray        托盘 UI：常驻系统托盘、四色状态、配置窗口、立即同步（登录自启）
  sync        后台同步，用法见 ddns-hosts-sync sync -h
  install     安装：目录/ACL + 默认配置 + 计划任务 + 托盘自启 + 首轮同步
  uninstall   卸载：注销任务/自启 + hosts 还原 + 数据目录清理
  version     打印版本（ldflags 注入，如 v0.1.0+<sha>）
`)
}

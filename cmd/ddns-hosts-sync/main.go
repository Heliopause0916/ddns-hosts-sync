// 命令 ddns-hosts-sync 是唯一分发点：按 argv 分派 tray|sync|install|uninstall|version
// （ARCHITECTURE.md §3.1，DSD §2.6 导入方向硬约束）。
//
// M0 实现 version 与子命令骨架；M1 实现 sync；M2b-1 实现 install/uninstall
// （tasks/platform 三平台）；tray 留待 M2b-2。
package main

import (
	"fmt"
	"os"
)

// version 由 ldflags 注入：go build -ldflags "-X main.version=v0.1.0+<sha>"
// 未注入时输出 "dev"。
var version = "dev"

func main() {
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
		// M2b-2 交付；本期仅骨架。
		fmt.Fprintln(os.Stderr, "tray: 未实现（M2b-2 交付）")
		os.Exit(1)
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
  tray        托盘 UI（未实现）
  sync        后台同步，用法见 ddns-hosts-sync sync -h
  install     安装：目录/ACL + 默认配置 + 计划任务 + 托盘自启 + 首轮同步
  uninstall   卸载：注销任务/自启 + hosts 还原 + 数据目录清理
  version     打印版本（ldflags 注入，如 v0.1.0+<sha>）
`)
}

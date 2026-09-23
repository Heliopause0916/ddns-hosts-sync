// 命令 ddns-hosts-sync 是唯一分发点：按 argv 分派 tray|sync|install|uninstall|version
// （ARCHITECTURE.md §3.1，DSD §2.6 导入方向硬约束）。
//
// M0 仅实现 version 与子命令骨架；其余子命令输出"未实现（M0 仅骨架）"并 exit 1。
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
	case "tray", "sync", "install", "uninstall":
		// M1/M2 实现；本期仅骨架。
		fmt.Fprintf(os.Stderr, "%s: 未实现（M0 仅骨架）\n", os.Args[1])
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
  sync        后台同步（未实现）
  install     安装（未实现）
  uninstall   卸载（未实现）
  version     打印版本（ldflags 注入，如 v0.1.0+<sha>）
`)
}

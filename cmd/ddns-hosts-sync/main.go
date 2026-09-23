// 命令 ddns-hosts-sync 是唯一分发点：按 argv 分派 tray|sync|install|uninstall|version
// （ARCHITECTURE.md §3.1，DSD §2.6 导入方向硬约束）。
//
// M0 仅实现 version 与子命令骨架；M1 实现 sync（命令行全链路）；其余子命令
// 输出"未实现"并 exit 1（M2 交付）。
package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/heliopause/ddns-hosts-sync/internal/sync"
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
	case "tray", "install", "uninstall":
		// M2 实现；本期仅骨架。
		fmt.Fprintf(os.Stderr, "%s: 未实现（M2 交付）\n", os.Args[1])
		os.Exit(1)
	default:
		usage()
		os.Exit(1)
	}
}

// runSync 解析 sync 子命令参数并执行一次完整同步循环（DSD §2.3 状态机）。
// 退出码：0 = 本轮循环完成（DNS/写盘失败已编码进 status.json）；
// 1 = 程序自身致命错误（state 目录不可写等，DSD §2.3 步骤 8）。
func runSync(args []string) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "./config.yaml", "配置文件路径")
	hostsPath := fs.String("hosts", "/etc/hosts", "hosts 文件路径（开发期显式传入临时文件，M2 platform 化后替换）")
	stateDir := fs.String("state", "./state", "状态目录（status.json/trigger.json）")
	logPath := fs.String("log", "./logs/sync.log", "日志文件路径")
	force := fs.Bool("force", false, "忽略间隔门，立即完整同步")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0 // -h 已打印用法，正常退出
		}
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "sync: 多余的参数: %v\n", fs.Args())
		return 1
	}
	_, err := sync.Run(sync.Options{
		ConfigPath:   *configPath,
		StateDir:     *stateDir,
		LogPath:      *logPath,
		HostsPath:    *hostsPath,
		Force:        *force,
		IntervalTick: 60, // 任务注册粒度固定 60s（DSD §1.1 注）
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync: %v\n", err)
		return 1
	}
	return 0
}

func usage() {
	fmt.Fprint(os.Stderr, `ddns-hosts-sync —— 通用 CNAME→hosts 同步工具

用法:
  ddns-hosts-sync <子命令>

子命令:
  tray        托盘 UI（未实现）
  sync        后台同步，用法见 ddns-hosts-sync sync -h
  install     安装（未实现）
  uninstall   卸载（未实现）
  version     打印版本（ldflags 注入，如 v0.1.0+<sha>）
`)
}

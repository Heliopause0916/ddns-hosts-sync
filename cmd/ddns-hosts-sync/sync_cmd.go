package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/heliopause/ddns-hosts-sync/internal/platform"
	"github.com/heliopause/ddns-hosts-sync/internal/sync"
)

// runSync 解析 sync 子命令参数并执行一次完整同步循环（DSD §2.3 状态机）。
// 退出码：0 = 本轮循环完成（DNS/写盘失败已编码进 status.json）；
// 1 = 程序自身致命错误（state 目录不可写等，DSD §2.3 步骤 8）。
func runSync(args []string) int {
	fs := flag.NewFlagSet("sync", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	configPath := fs.String("config", "./config.yaml", "配置文件路径")
	hostsPath := fs.String("hosts", "/etc/hosts", "hosts 文件路径（开发期显式传入临时文件，生产由 install 经 platform 路径注入）")
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
		// flushdns 职责自 M1 迁移至 platform（三平台实现），CLI 入口保持
		// M1 语义恒常注入；是否执行由 config.flush_dns 开关决定。
		FlushDNS: platform.New().FlushDNSCache,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "sync: %v\n", err)
		return 1
	}
	return 0
}

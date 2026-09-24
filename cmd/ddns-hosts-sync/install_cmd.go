package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/platform"
	"github.com/heliopause/ddns-hosts-sync/internal/sync"
	"github.com/heliopause/ddns-hosts-sync/internal/tasks"
)

// runInstall 执行一次安装（需管理员/UAC/sudo）：目录+ACL → 默认 config →
// 计划任务 → 托盘自启 → 首轮同步。幂等可重跑（升级路径：任务 /F 覆盖 /
// launchd unload+load / systemd 文件覆盖，config 已存在则保留）。
//
// --dir 覆盖数据根目录（开发/测试用；hosts 仍走平台默认路径）。
func runInstall(args []string) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "覆盖数据目录（开发/测试用）")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "install: 多余的参数: %v\n", fs.Args())
		return 1
	}

	p := platform.NewWithDataDir(*dir)

	// 1. 创建目录树 + ACL（先建后授权，DSD §7.4）。
	if err := p.SetupAllDirs(); err != nil {
		fmt.Fprintf(os.Stderr, "install: 初始化目录失败: %v\n", err)
		return 1
	}
	fmt.Printf("  数据目录: %s\n", p.DataDir())

	// 2. 生成默认 config（不存在才生成；存在则保留，用户配置不被覆盖）。
	cfgStatus := "已存在，保留"
	if _, err := os.Stat(p.ConfigPath()); err != nil {
		if !os.IsNotExist(err) {
			fmt.Fprintf(os.Stderr, "install: 检查配置失败: %v\n", err)
			return 1
		}
		if err := writeDefaultConfig(p.ConfigPath()); err != nil {
			fmt.Fprintf(os.Stderr, "install: 生成默认配置失败: %v\n", err)
			return 1
		}
		cfgStatus = "已生成默认配置"
	}
	fmt.Printf("  配置: %s（%s）\n", p.ConfigPath(), cfgStatus)

	// 3. 注册计划任务：平台不支持（如缺 systemctl）时提示并继续（务实降级）。
	spec := tasks.TaskSpec{Name: platform.TaskName, ExecPath: p.ExecPath(), Args: []string{"sync"}}
	if tm := tasks.New(); !tm.IsSupported() {
		fmt.Println("  计划任务 [ddns-hosts-sync]: 当前平台不支持计划任务，请手动配置每分钟触发")
	} else if err := p.InstallTask(spec); err != nil {
		fmt.Fprintf(os.Stderr, "install: 注册计划任务失败: %v\n", err)
		return 1
	} else {
		fmt.Println("  计划任务 [ddns-hosts-sync]: 已注册（每分钟触发，进程内按间隔执行）")
	}

	// 4. 托盘自启（用户态入口；tray 子命令 M2b-2 交付后随登录拉起）。
	if err := p.InstallTrayAutostart(); err != nil {
		fmt.Fprintf(os.Stderr, "install: 提示: 托盘自启注册失败（可忽略）: %v\n", err)
	} else {
		fmt.Println("  托盘自启: 已注册（tray 子命令于 M2b-2 交付后生效）")
	}

	// 5. 首轮同步（Force=true：安装即完整同步一次，hosts 立即可用）。
	st, err := sync.Run(sync.Options{
		ConfigPath:   p.ConfigPath(),
		StateDir:     p.StateDir(),
		TriggerPath:  p.TriggerPath(),
		LogPath:      p.LogPath(),
		HostsPath:    p.HostsPath(),
		Force:        true,
		IntervalTick: 60,
		FlushDNS:     p.FlushDNSCache,
	})
	if err != nil {
		fmt.Fprintf(os.Stderr, "install: 首轮同步失败: %v\n", err)
		return 1
	}
	fmt.Printf("  首轮同步: 完成（窗口=%s 条目=%d 写盘=%v）\n",
		st.SyncWindow, len(st.Entries), st.HostsBlock.LastWriteOK && st.Task.LastRunOK)
	if st.Task.LastError != "" {
		fmt.Printf("  提示: 同步告警：%s\n", st.Task.LastError)
	}

	fmt.Println("install 完成。后台同步由计划任务维护（免登录、免二次提权）；重新运行可幂等重装。")
	return 0
}

// writeDefaultConfig 以默认模板生成 config.yaml（仅当文件不存在，调用方已判定）。
func writeDefaultConfig(path string) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	data, err := config.DefaultTemplate()
	if err != nil {
		return err
	}
	return os.WriteFile(path, data, 0o644)
}

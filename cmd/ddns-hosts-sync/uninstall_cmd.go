package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/heliopause/ddns-hosts-sync/internal/hostsfile"
	"github.com/heliopause/ddns-hosts-sync/internal/platform"
	"github.com/heliopause/ddns-hosts-sync/internal/state"
)

// runUninstall 执行一次卸载（需管理员/UAC/sudo）：注销计划任务 → 移除托盘自启
// → 还原 hosts（块与上次同步状态一致则删除；不一致保留备份提示人工确认，
// ARCHITECTURE §4.3）→ 日志副本留存安装目录 → 清理数据目录。幂等可重跑。
//
// --dir 覆盖数据根目录（开发/测试用），hosts 路径与 install 一致走平台默认。
func runUninstall(args []string) int {
	fs := flag.NewFlagSet("uninstall", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	dir := fs.String("dir", "", "覆盖数据目录（开发/测试用）")
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "uninstall: 多余的参数: %v\n", fs.Args())
		return 1
	}

	p := platform.NewWithDataDir(*dir)

	// 1. 注销计划任务（内部失败已打印到 stderr，不阻断整体清理）。
	p.UninstallTask(platform.TaskName)
	fmt.Println("  计划任务 [ddns-hosts-sync]: 注销流程完成")

	// 2. 移除托盘自启。
	if err := p.RemoveTrayAutostart(); err != nil {
		fmt.Fprintf(os.Stderr, "uninstall: 提示: 移除托盘自启失败（可忽略）: %v\n", err)
	} else {
		fmt.Println("  托盘自启: 已移除")
	}

	// 3. 还原 hosts（DSD §4.3 卸载语义）。
	fmt.Printf("  hosts: %s\n", restoreHosts(p))

	// 4. 日志副本留存安装目录（ARCHITECTURE §3.1：卸载后日志可追溯）。
	keepLogCopy(p)

	// 5. 清理数据目录。
	if err := removeDataDir(p); err != nil {
		fmt.Fprintf(os.Stderr, "uninstall: 清理数据目录失败: %v\n", err)
		return 1
	}

	fmt.Println("uninstall 完成。hosts 中所有 ddns-hosts-sync 托管行已按上文提示处理。")
	return 0
}

// restoreHosts 实现 DSD §4.3 卸载还原语义：
//
//	当前 hosts 块 md5 == 最后一次 status.hosts_block.content_md5 → 删除整块（无损还原）；
//	不一致（外部修改过）→ 保留现状与备份，提示人工确认；无块 / 无 status 记录 → 跳过。
//
// 返回面向用户的结果描述（不返回 error：各分支均为可交代的终态）。
func restoreHosts(p platform.Platform) string {
	st, ok, err := state.ReadStatus(filepath.Join(p.StateDir(), "status.json"))
	if err != nil {
		return fmt.Sprintf("读取 status.json 失败，hosts 未改动（%v）", err)
	}
	if !ok {
		return "无同步记录，跳过 hosts 还原（保守不做修改）"
	}
	content, rerr := hostsfile.Read(p.HostsPath())
	if rerr != nil {
		if os.IsNotExist(rerr) {
			return "hosts 不存在，无需还原"
		}
		return fmt.Sprintf("读取 hosts 失败，未改动（%v）", rerr)
	}
	curMD5, merr := hostsfile.MD5Block(content)
	if merr != nil {
		return "hosts 无托管块，无需还原"
	}
	if curMD5 != st.HostsBlock.ContentMD5 {
		return "hosts 块与上次同步状态不一致（已被外部修改），保留现状与备份 hosts.bak-ddns-hosts-sync，请人工确认"
	}
	rest, _ := hostsfile.RemoveBlock(content)
	if werr := hostsfile.WriteAtomic(p.HostsPath(), rest, hostsfile.DetectEOL(content)); werr != nil {
		return fmt.Sprintf("还原 hosts 失败（需管理员权限）: %v", werr)
	}
	return "已删除托管块，hosts 已还原"
}

// keepLogCopy 将 sync.log 复制到可执行文件所在目录（"日志副本留存安装目录"，
// ARCHITECTURE §3.1）。数据目录不可用时/无日志时静默跳过。
func keepLogCopy(p platform.Platform) {
	data, err := os.ReadFile(p.LogPath())
	if err != nil {
		fmt.Println("  日志副本: 无日志可保留")
		return
	}
	exe := p.ExecPath()
	if exe == "" {
		fmt.Println("  日志副本: 无法定位安装目录，跳过")
		return
	}
	exeDir := filepath.Dir(exe)
	// 可执行文件位于数据目录内时（罕见调试场景）副本会随数据目录一起被删，
	// 此时跳过副本避免误导。
	if strings.HasPrefix(filepath.Clean(exe), filepath.Clean(p.DataDir())+string(os.PathSeparator)) {
		fmt.Println("  日志副本: 安装目录位于数据目录内，跳过（数据目录将被整体清理）")
		return
	}
	dst := filepath.Join(exeDir, "ddns-hosts-sync.sync.log")
	if err := os.WriteFile(dst, data, 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "uninstall: 提示: 日志副本写入失败（可忽略）: %v\n", err)
		return
	}
	fmt.Printf("  日志副本: %s\n", dst)
}

// removeDataDir 删除数据目录树（不存在视为成功，幂等）。防自杀护栏：
// 可执行文件若位于数据目录内（--dir 指向程序自身目录的调试场景）则跳过删除。
func removeDataDir(p platform.Platform) error {
	data := filepath.Clean(p.DataDir())
	if exe := p.ExecPath(); exe != "" &&
		strings.HasPrefix(filepath.Clean(exe), data+string(os.PathSeparator)) {
		fmt.Printf("  数据目录: 可执行文件位于数据目录内，跳过删除（保留 %s）\n", data)
		return nil
	}
	if err := os.RemoveAll(data); err != nil {
		return err
	}
	fmt.Printf("  数据目录: 已清理（%s）\n", data)
	return nil
}

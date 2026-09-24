//go:build darwin

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/heliopause/ddns-hosts-sync/internal/tasks"
)

// darwinPlatform macOS 平台实现（DSD §2.6 + ARCHITECTURE §5.2，"尽力支持"）。
// 数据目录 /usr/local/var/ddns-hosts-sync（root:staff 775），任务走 launchd
// LaunchDaemon（root），自启走 ~/Library/LaunchAgents（用户态）。
type darwinPlatform struct {
	machine
}

var _ Platform = (*darwinPlatform)(nil)

// staffGID macOS 默认组 staff 的 GID（20）。
const staffGID = 20

// newPlatform macOS 实现。
func newPlatform() *darwinPlatform {
	p := &darwinPlatform{}
	p.machine = machine{
		paths: resolveDarwinPaths(envMap()),
		exec:  execPath(),
	}
	return p
}

// FlushDNSCache `dscacheutil -flushcache; killall -HUP mDNSResponder`
// （ARCHITECTURE §5.2）。两命令任一成功即视为刷新成功；双败才报错。
func (p *darwinPlatform) FlushDNSCache() error {
	errs := make([]error, 0, 2)
	if err := exec.Command("dscacheutil", "-flushcache").Run(); err != nil {
		errs = append(errs, fmt.Errorf("dscacheutil: %w", err))
	}
	if err := exec.Command("killall", "-HUP", "mDNSResponder").Run(); err != nil {
		errs = append(errs, fmt.Errorf("killall: %w", err))
	}
	if len(errs) == 2 {
		return fmt.Errorf("macOS DNS 缓存刷新失败: %v; %v", errs[0], errs[1])
	}
	return nil
}

// SetupAllDirs 创建目录树并设置 root:staff / 775（ARCHITECTURE §5.2：
// chown root:staff + chmod 目录 775）。config.yaml 由后续 GUI 以 temp+rename
// 覆盖写入（同组可写目录 + staff 组成员默认），故 0644 不阻断 GUI 保存。
// 非 root 安装（调试场景）跳过 chown，仅保留 775。
func (p *darwinPlatform) SetupAllDirs() error {
	dirs := dataDirTree(p.paths.dataDir, p.paths.state)
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o775); err != nil {
			return fmt.Errorf("platform: 创建目录 %s 失败: %w", d, err)
		}
		// MkdirAll 受 umask 影响，显式 chmod 保证组写位（GUI 用户为 staff）。
		if err := os.Chmod(d, 0o775); err != nil {
			return fmt.Errorf("platform: chmod %s 失败: %w", d, err)
		}
	}
	if os.Geteuid() == 0 {
		for _, d := range dirs {
			if err := os.Chown(d, 0, staffGID); err != nil {
				return fmt.Errorf("platform: chown %s root:staff 失败: %w", d, err)
			}
		}
	}
	return nil
}

// InstallTrayAutostart 写 ~/Library/LaunchAgents/com.ddns-hosts-sync.tray.plist
// 并装载（登录自启；tray 子命令 M2b-2 交付后生效）。plist 复用 tasks 包纯
// 生成函数（标名 com.ddns-hosts-sync.tray，ProgramArguments=[exec, tray]，
// RunAtLoad=true），避免自启文件与任务文件格式分叉。
func (p *darwinPlatform) InstallTrayAutostart() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("platform: 解析用户主目录失败: %w", err)
	}
	dir := filepath.Join(home, "Library", "LaunchAgents")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("platform: 创建 LaunchAgents 目录失败: %w", err)
	}
	payload, err := tasks.BuildLaunchdPlist(tasks.TaskSpec{
		Name:     "ddns-hosts-sync.tray", // Label=com.ddns-hosts-sync.tray
		ExecPath: p.exec,
		Args:     []string{"tray"},
	})
	if err != nil {
		return fmt.Errorf("platform: 生成自启 plist 失败: %w", err)
	}
	path := filepath.Join(dir, "com.ddns-hosts-sync.tray.plist")
	if err := os.WriteFile(path, payload, 0o644); err != nil {
		return fmt.Errorf("platform: 写自启 plist %s 失败: %w", path, err)
	}
	_ = exec.Command("launchctl", "load", "-w", path).Run() // 尽力装载，失败不阻断（下轮登录生效）
	return nil
}

// RemoveTrayAutostart 卸载并删除 LaunchAgent plist（不存在视为成功，幂等）。
func (p *darwinPlatform) RemoveTrayAutostart() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("platform: 解析用户主目录失败: %w", err)
	}
	path := filepath.Join(home, "Library", "LaunchAgents", "com.ddns-hosts-sync.tray.plist")
	_ = exec.Command("launchctl", "unload", path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("platform: 删除自启 plist %s 失败: %w", path, err)
	}
	return nil
}

// IsAdmin macOS euid==0（安装经 sudo 管理员授权）。
func (p *darwinPlatform) IsAdmin() bool { return os.Geteuid() == 0 }

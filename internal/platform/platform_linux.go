//go:build linux

package platform

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// linuxPlatform Linux 平台实现（DSD §2.6 + ARCHITECTURE §5.3）。
// Linux 为可选支持平台：数据目录走 XDG（用户态，免 root 可测全流程），
// 计划任务走 systemd system timer（需 root）；自启写 ~/.config/autostart。
type linuxPlatform struct {
	machine
}

var _ Platform = (*linuxPlatform)(nil)

// newPlatform Linux 实现（newPlatform 名由各 build-tagged 文件各自定义）。
func newPlatform() *linuxPlatform {
	p := &linuxPlatform{}
	p.machine = machine{
		paths: resolveLinuxPaths(envMap()),
		exec:  execPath(),
	}
	return p
}

// FlushDNSCache Linux best-effort DNS 缓存刷新：尝试 resolvectl flush-caches
// 或 systemd-resolve --flush-caches，全部失败才报错（调用方仅 warn）。
// 自 M1 的 internal/sync.flushDNSCache 迁移（原实现已删除，见 sync.go）。
func (p *linuxPlatform) FlushDNSCache() error {
	cmds := [][]string{
		{"resolvectl", "flush-caches"},
		{"systemd-resolve", "--flush-caches"},
	}
	for _, c := range cmds {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		if err := exec.Command(c[0], c[1:]...).Run(); err == nil {
			return nil
		}
	}
	return errors.New("resolvectl/systemd-resolve 均不可用或执行失败")
}

// SetupAllDirs 创建数据目录树（XDG 用户目录，0755；无需 ACL 特殊处理——
// Linux 平台上单用户部署，目录天然归属当前用户）。
func (p *linuxPlatform) SetupAllDirs() error {
	for _, d := range dataDirTree(p.paths.dataDir, p.paths.state) {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("platform: 创建目录 %s 失败: %w", d, err)
		}
	}
	return nil
}

// InstallTrayAutostart 写 ~/.config/autostart/ddns-hosts-sync.desktop
// （ARCHITECTURE §5.3）。tray 子命令于 M2b-2 交付，此处即写入入口，
// 届时托盘进程随登录自动拉起。
func (p *linuxPlatform) InstallTrayAutostart() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("platform: 解析用户主目录失败: %w", err)
	}
	dir := filepath.Join(home, ".config", "autostart")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return fmt.Errorf("platform: 创建自启目录 %s 失败: %w", dir, err)
	}
	content := "[Desktop Entry]\n" +
		"Type=Application\n" +
		"Name=ddns-hosts-sync\n" +
		"Comment=ddns-hosts-sync 托盘\n" +
		desktopExecLine(p.exec) + "\n" + // Exec 路径加引号（S15：含空格安装路径可用）
		"Terminal=false\n" +
		"X-GNOME-Autostart-enabled=true\n"
	path := filepath.Join(dir, "ddns-hosts-sync.desktop")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		return fmt.Errorf("platform: 写自启文件 %s 失败: %w", path, err)
	}
	return nil
}

// RemoveTrayAutostart 删除自启 desktop 文件（不存在视为成功，幂等）。
func (p *linuxPlatform) RemoveTrayAutostart() error {
	home, err := os.UserHomeDir()
	if err != nil {
		return fmt.Errorf("platform: 解析用户主目录失败: %w", err)
	}
	path := filepath.Join(home, ".config", "autostart", "ddns-hosts-sync.desktop")
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("platform: 删除自启文件 %s 失败: %w", path, err)
	}
	return nil
}

// IsAdmin Linux 以 euid==0 判定（sudo 安装场景）。
func (p *linuxPlatform) IsAdmin() bool { return os.Geteuid() == 0 }

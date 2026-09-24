//go:build darwin

package tasks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

// darwinManager launchd LaunchDaemon 封装（ARCHITECTURE §5.2）：
//   - 写 /Library/LaunchDaemons/com.ddns-hosts-sync.plist（RunAtLoad=true +
//     StartInterval=60，与 Windows 任务同构）；
//   - load：先 launchctl unload（幂等重装）再 load -w（忽略 unload 失败）；
//   - 卸载：unload + 删除 plist；触发：launchctl kickstart -k system/<label>。
//
// 写 /Library/LaunchDaemons 需 root（安装经 sudo/管理员授权，§5.2）。
type darwinManager struct{}

const launchDaemonDir = "/Library/LaunchDaemons"

// New 返回 macOS launchd 实现。
func New() Manager {
	return darwinManager{}
}

// IsSupported macOS 恒为 true（launchd 为系统组件）。
func (darwinManager) IsSupported() bool { return true }

// plistPath launchd plist 文件路径：/Library/LaunchDaemons/com.ddns-hosts-sync.plist。
func plistPath(name string) string {
	return filepath.Join(launchDaemonDir, launchLabel(name)+".plist")
}

// Install 写入 plist 并通过 launchctl 装载（重装路径先 unload，幂等）。
func (m darwinManager) Install(spec TaskSpec) error {
	content, err := BuildLaunchdPlist(spec)
	if err != nil {
		return fmt.Errorf("launchd: 生成 plist 失败: %w", err)
	}
	path := plistPath(spec.Name)
	// 装载态先用 unload 清理（重装/升级路径；目标未装载时 unload 报错，可忽略）。
	_ = exec.Command("launchctl", "unload", path).Run()
	if err := os.WriteFile(path, content, 0o644); err != nil {
		return fmt.Errorf("launchd: 写 %s 失败: %w", path, err)
	}
	if out, err := exec.Command("launchctl", "load", "-w", path).CombinedOutput(); err != nil {
		return fmt.Errorf("launchd: load %s 失败: %v（输出: %s）", path, err, string(out))
	}
	return nil
}

// Uninstall 卸载并删除 plist；目标不存在视为成功（幂等）。
func (m darwinManager) Uninstall(name string) error {
	path := plistPath(name)
	_ = exec.Command("launchctl", "unload", path).Run()
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("launchd: 删除 %s 失败: %w", path, err)
	}
	return nil
}

// Trigger 立即触发一次（system/ 域 kickstart -k 重启实例）。失败返回错误，
// 由 platform 层按 DSD 静默。
func (m darwinManager) Trigger(name string) error {
	label := launchLabel(name)
	out, err := exec.Command("launchctl", "kickstart", "-k", "system/"+label).CombinedOutput()
	if err != nil {
		return fmt.Errorf("launchd: kickstart %s 失败: %v（输出: %s）", label, err, string(out))
	}
	return nil
}

// Exists 以 plist 文件是否存在判定任务注册态。
func (m darwinManager) Exists(name string) (bool, error) {
	_, err := os.Stat(plistPath(name))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

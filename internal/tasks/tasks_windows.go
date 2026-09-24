//go:build windows

package tasks

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// windowsManager schtasks 命令封装（ARCHITECTURE §5.1、DSD §5.2）。
//   - Install：BuildTaskXML 生成 XML → UTF-16LE+BOM 写入临时文件 →
//     `schtasks /Create /TN <name> /XML <file> /F`（/F 强制覆盖，幂等）；
//   - Uninstall：`schtasks /Delete /TN <name> /F`；
//   - Trigger：`schtasks /Run /TN <name>`（失败由 platform 层静默）；
//   - Exists：`schtasks /Query /TN <name>`，退出码/输出解析。
type windowsManager struct{}

// New 返回 Windows 计划任务实现（schtasks 为 Windows 系统自带，恒可支持）。
func New() Manager {
	return windowsManager{}
}

// IsSupported Windows 恒为 true（schtasks.exe 属系统组件）。
func (windowsManager) IsSupported() bool { return true }

// Install 注册（覆盖）计划任务。临时 XML 文件用后即删。
func (m windowsManager) Install(spec TaskSpec) error {
	doc, err := BuildTaskXML(spec)
	if err != nil {
		return fmt.Errorf("schtasks: 生成 XML 失败: %w", err)
	}
	tmp, err := os.CreateTemp("", "ddns-hosts-sync-schtasks-*.xml")
	if err != nil {
		return fmt.Errorf("schtasks: 创建临时 XML 失败: %w", err)
	}
	tmpPath := tmp.Name()
	defer func() {
		_ = tmp.Close()
		_ = os.Remove(tmpPath)
	}()
	// schtasks 要求 XML 文件为 UTF-16LE（带 BOM）或 ASCII；路径可能含中文
	// （DSD §7.3），统一编码为 UTF-16LE + BOM（0xEF 0xBB 0xBF 是 UTF-8 BOM，
	// UTF-16LE BOM 为 FF FE）。
	payload := append([]byte("<?xml version=\"1.0\" encoding=\"UTF-16\"?>\r\n"), doc...)
	le := utf16EncodeLE(payload)
	if _, err := tmp.Write(le); err != nil {
		return fmt.Errorf("schtasks: 写临时 XML 失败: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("schtasks: 关闭临时 XML 失败: %w", err)
	}

	out, err := exec.Command("schtasks", "/Create", "/TN", spec.Name,
		"/XML", tmpPath, "/F").CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /Create 失败: %v（输出: %s）", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall 注销任务；目标不存在视为成功（幂等）。
func (m windowsManager) Uninstall(name string) error {
	out, err := exec.Command("schtasks", "/Delete", "/TN", name, "/F").CombinedOutput()
	if err != nil {
		if !isTaskNotFound(out, err) {
			return fmt.Errorf("schtasks /Delete 失败: %v（输出: %s）", err, strings.TrimSpace(string(out)))
		}
	}
	return nil
}

// Trigger 立即触发一次（尽力而为；失败返回错误，platform 层按 DSD 静默）。
func (m windowsManager) Trigger(name string) error {
	out, err := exec.Command("schtasks", "/Run", "/TN", name).CombinedOutput()
	if err != nil {
		return fmt.Errorf("schtasks /Run 失败: %v（输出: %s）", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Exists 判定任务注册状态。
func (m windowsManager) Exists(name string) (bool, error) {
	out, err := exec.Command("schtasks", "/Query", "/TN", name).CombinedOutput()
	if err == nil {
		return true, nil
	}
	if isTaskNotFound(out, err) {
		return false, nil
	}
	return false, fmt.Errorf("schtasks /Query 失败: %v（输出: %s）", err, strings.TrimSpace(string(out)))
}

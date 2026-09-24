//go:build linux

package tasks

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// linuxManager systemd system timer 封装：
//   - 写 /etc/systemd/system/ddns-hosts-sync.service（Type=oneshot，ExecStart
//     指向程序）与 .timer（OnBootSec=60 + OnUnitActiveSec=60，每 1 分钟触发）；
//     systemd 负责拉起进程，进程内间隔门（interval_minutes）由 sync 承担
//     （DSD §1.1："任务注册粒度固定 1 分钟"）；
//   - enable --now 注册为系统级 timer（timers.target，开机自启）；
//   - 卸载：disable --now → 删除 unit+timer 文件 → daemon-reload；
//   - 触发：systemctl start <service> 立即执行一次。
//
// 写 /etc/systemd/system 需 root；容器/桌面发行版缺 systemctl 时 New() 返回
// IsSupported=false 的降级实现（install 转而为提示，保持务实）。
type linuxManager struct {
	supported bool
}

const (
	linuxUnitName    = "ddns-hosts-sync.service"
	linuxTimerName   = "ddns-hosts-sync.timer"
	linuxUnitsDir    = "/etc/systemd/system"
	linuxServiceDesc = "ddns-hosts-sync background sync"
)

// New 探测 systemctl：可用则返回 systemd 实现，否则返回 IsSupported=false
// 的降级实现（不视为错误，install 子命令提示手动配置）。
func New() Manager {
	if _, err := exec.LookPath("systemctl"); err != nil {
		return linuxManager{supported: false}
	}
	return linuxManager{supported: true}
}

// IsSupported 是否具备 systemd 计划任务能力。
func (m linuxManager) IsSupported() bool { return m.supported }

// Install 写 unit+timer 并 enable --now（幂等：文件覆盖 + 重复 enable 无害）。
func (m linuxManager) Install(spec TaskSpec) error {
	if !m.supported {
		return fmt.Errorf("systemd 不可用（缺少 systemctl），无法注册计划任务")
	}
	unit := fmt.Sprintf(`[Unit]
Description=%s
After=network-online.target
Wants=network-online.target

[Service]
Type=oneshot
ExecStart=%s sync
`, linuxServiceDesc, systemdQuoteExec(spec.ExecPath))
	timer := `[Unit]
Description=ddns-hosts-sync periodic trigger

[Timer]
OnBootSec=60
OnUnitActiveSec=60
AccuracySec=5s
Unit=` + linuxUnitName + `

[Install]
WantedBy=timers.target
`
	unitPath := filepath.Join(linuxUnitsDir, linuxUnitName)
	timerPath := filepath.Join(linuxUnitsDir, linuxTimerName)
	if err := os.WriteFile(unitPath, []byte(unit), 0o644); err != nil {
		return fmt.Errorf("systemd: 写 %s 失败: %w", unitPath, err)
	}
	if err := os.WriteFile(timerPath, []byte(timer), 0o644); err != nil {
		return fmt.Errorf("systemd: 写 %s 失败: %w", timerPath, err)
	}
	if out, err := exec.Command("systemctl", "daemon-reload").CombinedOutput(); err != nil {
		return fmt.Errorf("systemd: daemon-reload 失败: %v（输出: %s）", err, strings.TrimSpace(string(out)))
	}
	if out, err := exec.Command("systemctl", "enable", "--now", linuxTimerName).CombinedOutput(); err != nil {
		return fmt.Errorf("systemd: enable --now 失败: %v（输出: %s）", err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Uninstall 停用并删除 timer/unit 文件（幂等：目标缺失视为成功）。
func (m linuxManager) Uninstall(name string) error {
	_ = exec.Command("systemctl", "disable", "--now", linuxTimerName).Run()
	for _, f := range []string{linuxUnitName, linuxTimerName} {
		p := filepath.Join(linuxUnitsDir, f)
		if err := os.Remove(p); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("systemd: 删除 %s 失败: %w", p, err)
		}
	}
	if m.supported {
		_ = exec.Command("systemctl", "daemon-reload").Run()
	}
	return nil
}

// Trigger 立即运行一次 service（.timer 每周复触发本就兜底；start 为尽力即时）。
func (m linuxManager) Trigger(name string) error {
	out, err := exec.Command("systemctl", "start", linuxUnitName).CombinedOutput()
	if err != nil {
		return fmt.Errorf("systemd: start %s 失败: %v（输出: %s）", linuxUnitName, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// Exists 以 timer unit 文件是否存在判定注册态（安装与卸载均以文件为准）。
func (m linuxManager) Exists(name string) (bool, error) {
	_, err := os.Stat(filepath.Join(linuxUnitsDir, linuxTimerName))
	if err == nil {
		return true, nil
	}
	if os.IsNotExist(err) {
		return false, nil
	}
	return false, err
}

// systemdQuoteExec 按 systemd.syntax 转义 ExecStart 路径：空格与引号需
// 反斜杠转义、% 转义为 %%（systemd 说明符）。路径含 '#' ';' 前缀等场景
// 在实际安装目录下罕见，按最小必要转义处理。
func systemdQuoteExec(p string) string {
	var b strings.Builder
	for _, r := range p {
		switch r {
		case ' ', '"', '\'', '\\':
			b.WriteByte('\\')
			b.WriteRune(r)
		case '%':
			b.WriteString("%%")
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

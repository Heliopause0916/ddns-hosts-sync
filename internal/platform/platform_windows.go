//go:build windows

package platform

import (
	"fmt"
	"os"
	"os/exec"
	"strconv"
)

// windowsPlatform Windows 平台实现（DSD §2.6 + ARCHITECTURE §5.1）。
// 主目标平台：ACL 用 icacls 命令（本机自带），注册表用 reg.exe 命令封装
// （零新增依赖——Go 标准库无注册表 API，golang.org/x/sys/windows 需网络拉取，
// 开发沙箱受限，故用命令行封装并注释说明取舍，DSD §7 约束内）。
type windowsPlatform struct {
	machine
}

var _ Platform = (*windowsPlatform)(nil)

// runKey HKCU Run 注册表项（登录自启，免提权）。
const runKey = `HKCU\Software\Microsoft\Windows\CurrentVersion\Run`

// newPlatform Windows 实现。
func newPlatform() *windowsPlatform {
	p := &windowsPlatform{}
	p.machine = machine{
		paths: resolveWindowsPaths(envMap()),
		exec:  execPath(),
	}
	return p
}

// FlushDNSCache `ipconfig /flushdns`：只判 err，不解析 stdout（中文系统为
// GBK 输出，解析必乱码，DSD §7.7）。无变量传入，省略 cmd /C 包装。
func (p *windowsPlatform) FlushDNSCache() error {
	return exec.Command("ipconfig", "/flushdns").Run()
}

// SetupAllDirs 创建目录树并按 DSD §7.4 顺序设置 ACL（先建目录再授权）：
//
//  1. 根 %ProgramData%\ddns-hosts-sync：SYSTEM F / Administrators F /
//     Users R+列目录（现有目录用 /T 递归收口）；
//  2. config\：追加 Users:(OI)(CI)M（GUI 免提权写配置与 trigger.json，B2）；
//  3. state\、logs\：重新断言 SYSTEM F / Administrators F / Users R
//     （子目录收口，覆盖父目录 config 继承下来的 M）。
//
// ACL 规则由 windowsACLRules 纯函数装配（B1：逐参传 argv，杜绝整串
// 加引号导致的 Invalid parameter(s)）。
func (p *windowsPlatform) SetupAllDirs() error {
	dirs := dataDirTree(p.paths.dataDir, p.paths.state)
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("platform: 创建目录 %s 失败: %w", d, err)
		}
	}
	for _, r := range windowsACLRules(p.paths.dataDir) {
		if err := runIcacls(r.dir, r.args...); err != nil {
			return err
		}
	}
	return nil
}

// runIcacls 执行一次 icacls <dir> <args...>；args 逐段传参（B1），
// 失败返回带输出的错误上下文。
func runIcacls(dir string, args ...string) error {
	out, err := exec.Command("icacls", append([]string{dir}, args...)...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("platform: icacls %s 失败: %v（输出: %s）", dir, err, out)
	}
	return nil
}

// InstallTrayAutostart 写 HKCU Run（reg.exe add，零依赖封装——标准库无注册表
// API；x/sys/windows 需拉取网络依赖，本沙箱离线，故选 reg.exe 提升务实性）。
// 值形如 `"C:\...\ddns-hosts-sync.exe" tray`；tray 子命令 M2b-2 交付后生效。
func (p *windowsPlatform) InstallTrayAutostart() error {
	value := strconv.Quote(p.exec) + " tray"
	out, err := exec.Command("reg", "add", runKey,
		"/v", TaskName, "/t", "REG_SZ", "/d", value, "/f").CombinedOutput()
	if err != nil {
		return fmt.Errorf("platform: reg add HKCU Run 失败: %v（输出: %s）", err, out)
	}
	return nil
}

// RemoveTrayAutostart 删除 HKCU Run 值（reg delete；值不存在回归 code 1，
// 幂等语义下忽略）。
func (p *windowsPlatform) RemoveTrayAutostart() error {
	_ = exec.Command("reg", "delete", runKey, "/v", TaskName, "/f").Run()
	return nil
}

// IsAdmin 用 `net session` 探测（仅管理员可调用成功）；失败判定为非管理员。
func (p *windowsPlatform) IsAdmin() bool {
	return exec.Command("net", "session").Run() == nil
}

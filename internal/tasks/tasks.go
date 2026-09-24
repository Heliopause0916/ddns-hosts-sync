// Package tasks 提供计划任务三平台接口（DSD §2.5）。
//
// Manager 依据 runtime.GOOS 编译期选择（build tag 分文件）：
//   - Windows：schtasks 命令封装（XML 由 xmgen.go 纯函数生成，注册
//     /Create /XML；注销 /Delete；触发 /Run；查询 /Query）；
//   - macOS：launchd LaunchDaemon plist（/Library/LaunchDaemons/...）；
//   - Linux：systemd system timer（unit + timer 文件，OnUnitActiveSec=60
//     每分钟触发，进程内间隔门由 sync 承担）；
//   - 其它 OS：IsSupported()=false 的降级实现。
//
// 导入方向：tasks 不依赖 platform（DSD §2.6 硬约束）。
package tasks

// TaskSpec 计划任务注册规格（DSD §2.5）：Name 为注册名 ddns-hosts-sync，
// ExecPath 为程序绝对路径，Args 固定 ["sync"]。SYSTEM 账户无 UserProfile，
// 所有路径绝对化、程序不依赖 CWD（DSD §5.2/§7.3）。
type TaskSpec struct {
	Name     string   // 注册名：ddns-hosts-sync
	ExecPath string   // 程序绝对路径
	Args     []string // ["sync"]
}

// Manager 计划任务管理器接口（DSD §2.5）。
type Manager interface {
	// Install 注册/覆盖计划任务（幂等：Windows /F 强制覆盖；launchd plist
	// 重写 + unload/load；systemd unit+timer 重写 + enable --now）。
	Install(spec TaskSpec) error
	// Uninstall 注销计划任务（幂等：目标不存在视为成功）。
	Uninstall(name string) error
	// Trigger 立即触发一次（schtasks /run / 系统级 kickstart / systemctl start）。
	// 尽力而为：失败返回错误，由上层决定是否静默。
	Trigger(name string) error
	// Exists 判定任务是否已注册。
	Exists(name string) (bool, error)
	// IsSupported 当前 OS 是否具备计划任务能力（缺失 systemctl 等场景返回
	// false，install 子命令转而为提示，不视为致命错误）。
	IsSupported() bool
}

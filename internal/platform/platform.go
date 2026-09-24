// Package platform 提供平台抽象（DSD §2.6）：路径、系统动作、任务委托、自启、
// 安装期目录/ACL。三份实现按 build tag 编译期选择（platform_windows.go /
// platform_darwin.go / platform_linux.go），另附其它 OS 的降级实现。
//
// 导入方向硬约束：被 cmd 与 sync/tray 消费；tasks 不反向依赖本包（DSD §2.6）。
package platform

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/heliopause/ddns-hosts-sync/internal/tasks"
)

// TaskName 计划任务/自启注册名（DSD §2.5：ddns-hosts-sync）。
const TaskName = "ddns-hosts-sync"

// Platform 平台抽象接口（DSD §2.6 方法集）。
type Platform interface {
	// ── 路径 ──
	HostsPath() string
	DataDir() string     // %ProgramData%\ddns-hosts-sync / /usr/local/var/ddns-hosts-sync / XDG
	ConfigPath() string  // DataDir/config/config.yaml
	TriggerPath() string // DataDir/config/trigger.json（B2：自 state\ 迁至 config\，GUI 免提权写）
	StateDir() string
	LogPath() string
	// ── 系统动作 ──
	FlushDNSCache() error
	// ── 任务（委托 tasks.Manager）──
	InstallTask(spec tasks.TaskSpec) error
	UninstallTask(name string)
	TriggerTask(name string)
	TaskExists(name string) (bool, error)
	// ── 自启/GUI（tray 落位 M2b-2，此处写入入口即可）──
	InstallTrayAutostart() error
	RemoveTrayAutostart() error
	// ── 安装期 ──
	SetupAllDirs() error // 创建目录树 + 设置 ACL（先建后授权，DSD §7.4）
	ExecPath() string    // os.Executable()
	IsAdmin() bool
}

// pathSet 平台解析出的全部路径（hosts 除外均为 DataDir 派生）。
type pathSet struct {
	hosts   string
	dataDir string
	config  string
	trigger string // DataDir/config/trigger.json（B2 落点）
	state   string
	log     string
}

// machine 公共内嵌载体：路径 + 可执行文件路径 + 三平台一致的方法
// （路径访问器、任务委托、rebasing）。OS 差异方法在各自 build-tagged 文件。
type machine struct {
	paths pathSet
	exec  string
}

// HostsPath 返回 hosts 文件路径。
func (m *machine) HostsPath() string { return m.paths.hosts }

// DataDir 返回数据根目录。
func (m *machine) DataDir() string { return m.paths.dataDir }

// ConfigPath 返回 config.yaml 路径。
func (m *machine) ConfigPath() string { return m.paths.config }

// TriggerPath 返回 trigger.json 路径（DataDir/config/trigger.json，B2）：
// config\ 目录 DSD §5.1 已给 Users M（GUI 免提权写配置），触发文件随 config
// 同目录可写；不再放置 state\（state\ 对 Users 只读，写 trigger 必 Access
// Denied——审查 B2）。
func (m *machine) TriggerPath() string { return m.paths.trigger }

// StateDir 返回 status.json 所在目录。
func (m *machine) StateDir() string { return m.paths.state }

// LogPath 返回 sync.log 路径。
func (m *machine) LogPath() string { return m.paths.log }

// ExecPath 返回当前可执行文件绝对路径（解析失败返回 ""）。
func (m *machine) ExecPath() string { return m.exec }

// rebase 以 dir 为数据根重算派生路径（install/uninstall --dir 开发/测试用；
// hosts 路径保持平台默认，不受 --dir 影响）。
func (m *machine) rebase(dir string) {
	if dir == "" {
		return
	}
	m.paths.dataDir = dir
	m.paths.config = filepath.Join(dir, "config", "config.yaml")
	m.paths.trigger = filepath.Join(dir, "config", "trigger.json")
	m.paths.state = filepath.Join(dir, "state")
	m.paths.log = filepath.Join(dir, "logs", "sync.log")
}

// InstallTask 委托 tasks.Manager 注册计划任务（DSD §2.6）。
func (m *machine) InstallTask(spec tasks.TaskSpec) error { return tasks.New().Install(spec) }

// UninstallTask 委托注销计划任务；失败打印到 stderr（卸载流程为多步骤
// 尽力清理，单项失败不阻断整体，DSD §2.6 签名即无 error 返回）。
func (m *machine) UninstallTask(name string) {
	if err := tasks.New().Uninstall(name); err != nil {
		fmt.Fprintf(os.Stderr, "platform: 注销任务 %s 失败: %v\n", name, err)
	}
}

// TriggerTask 委托立即触发；失败静默（DSD §4.5：普通用户对高级别任务 /run
// 常被拒，由下一分钟窗口兜底）。
func (m *machine) TriggerTask(name string) {
	_ = tasks.New().Trigger(name)
}

// TaskExists 委托查询任务注册态（GUI 启动自检用）。
func (m *machine) TaskExists(name string) (bool, error) { return tasks.New().Exists(name) }

// execPath 解析当前可执行文件绝对路径。
func execPath() string {
	p, err := os.Executable()
	if err != nil {
		return ""
	}
	return p
}

// New 返回当前 OS 的平台实现（build tag 编译期选择，DSD §2.6）。
func New() Platform { return NewWithDataDir("") }

// NewWithDataDir 返回以 dir 为数据根的平台实现（install/uninstall --dir
// 开发/测试用；dir 为空时走平台默认路径）。
func NewWithDataDir(dir string) Platform {
	p := newPlatform()
	if dir != "" {
		p.rebase(dir)
	}
	return p
}

// ---------------------------------------------------------------------------
// 路径纯函数（任意平台可单测：注入 env 映射断言各 OS 路径构造）
// ---------------------------------------------------------------------------

// resolveWindowsPaths 构造 Windows 路径（DSD §2.6/§5.1）：
//   - hosts：%SystemRoot%\System32\drivers\etc\hosts；
//   - 数据根：%ProgramData%\ddns-hosts-sync。
//
// env 注入 SystemRoot/ProgramData；缺失时回落 C:\Windows / C:\ProgramData。
// Windows 路径用显式 '\' 拼接（不依赖 filepath.Join，保证测试平台一致性）。
func resolveWindowsPaths(env map[string]string) pathSet {
	sysRoot := strings.TrimRight(strings.TrimSpace(env["SystemRoot"]), `\`)
	if sysRoot == "" {
		sysRoot = `C:\Windows`
	}
	progData := strings.TrimRight(strings.TrimSpace(env["ProgramData"]), `\`)
	if progData == "" {
		progData = `C:\ProgramData`
	}
	data := progData + `\ddns-hosts-sync`
	return pathSet{
		hosts:   sysRoot + `\System32\drivers\etc\hosts`,
		dataDir: data,
		config:  data + `\config\config.yaml`,
		trigger: data + `\config\trigger.json`,
		state:   data + `\state`,
		log:     data + `\logs\sync.log`,
	}
}

// resolveDarwinPaths 构造 macOS 路径（ARCHITECTURE §5.2）：数据根
// /usr/local/var/ddns-hosts-sync。env 目前仅占位（无环境依赖常量路径），
// 保留形参便于未来演进与统一测试形态。
func resolveDarwinPaths(env map[string]string) pathSet {
	data := `/usr/local/var/ddns-hosts-sync`
	return pathSet{
		hosts:   `/etc/hosts`,
		dataDir: data,
		config:  filepath.Join(data, "config", "config.yaml"),
		trigger: filepath.Join(data, "config", "trigger.json"),
		state:   filepath.Join(data, "state"),
		log:     filepath.Join(data, "logs", "sync.log"),
	}
}

// resolveLinuxPaths 构造 Linux 路径：数据根为
// ${XDG_DATA_HOME:-$HOME/.local/share}/ddns-hosts-sync，hosts 为 /etc/hosts。
// env 注入 XDG_DATA_HOME/HOME。
func resolveLinuxPaths(env map[string]string) pathSet {
	xdg := strings.TrimSpace(env["XDG_DATA_HOME"])
	if xdg == "" {
		xdg = filepath.Join(strings.TrimSpace(env["HOME"]), ".local", "share")
	}
	data := filepath.Join(xdg, "ddns-hosts-sync")
	return pathSet{
		hosts:   `/etc/hosts`,
		dataDir: data,
		config:  filepath.Join(data, "config", "config.yaml"),
		trigger: filepath.Join(data, "config", "trigger.json"),
		state:   filepath.Join(data, "state"),
		log:     filepath.Join(data, "logs", "sync.log"),
	}
}

// envMap 将 os.Environ 成对转换为 map（路径解析注入用）。
func envMap() map[string]string {
	m := make(map[string]string, 8)
	for _, kv := range os.Environ() {
		if i := strings.IndexByte(kv, '='); i > 0 {
			m[kv[:i]] = kv[i+1:]
		}
	}
	return m
}

// ---------------------------------------------------------------------------
// 安装目录树 / 桌面自启 Exec（纯函数，任意平台可单测）
// ---------------------------------------------------------------------------

// dataDirTree DataDir 下的安装目录集合（根 + config/state/logs 子目录）。
// 三平台 SetupAllDirs 共用（S14：unsupported 实现此前漏建 dataDir 本身与
// logs 子目录，统一由本函数保证形状一致）。
func dataDirTree(dataDir, stateDir string) []string {
	return []string{dataDir, filepath.Join(dataDir, "config"), stateDir, filepath.Join(dataDir, "logs")}
}

// desktopExecLine 组装 Linux/其它桌面自启文件的 Exec 行：可执行路径加引号
// （安装目录含空格时 Exec= 解析不被截断，S15），后随固定参数 "tray"。
func desktopExecLine(execPath string) string {
	return "Exec=" + strconv.Quote(execPath) + " tray"
}

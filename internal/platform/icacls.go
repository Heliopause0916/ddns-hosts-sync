package platform

// icaclsRule 一次 icacls 调用的完整参数：dir 为授权目标目录，args 为逐段
// ACL 参数（B1 修复：此前整串 ACL 作为单个 argv 传给 exec.Command，含空格
// 会被整体加引号导致 icacls "Invalid parameter(s)"；必须逐段传参）。
type icaclsRule struct {
	dir  string
	args []string
}

// windowSID 内置 SID 文本（避免本地化系统组名差异，DSD §7.4）。
const (
	sidSystem         = "*S-1-5-18"     // SYSTEM
	sidAdministrators = "*S-1-5-32-544" // Administrators
	sidUsers          = "*S-1-5-32-545" // Users
)

// restrictArgs 收口规则：断继承 + SYSTEM F / Administrators F / Users R，
// /T 递归到现有子项（根/state/logs 共用）。
func restrictArgs() []string {
	return []string{
		"/inheritance:r",
		"/grant:r", sidSystem + ":(OI)(CI)F",
		"/grant:r", sidAdministrators + ":(OI)(CI)F",
		"/grant:r", sidUsers + ":(OI)(CI)R",
		"/T",
	}
}

// usersModifyArgs config\ 追加 Users M（GUI 免提权写 config.yaml 与
// trigger.json；B2 依赖此规则保证 config\ 下触发文件可写）。
func usersModifyArgs() []string {
	return []string{
		"/grant:r", sidUsers + ":(OI)(CI)M",
		"/T",
	}
}

// windowsACLRules 依据 DSD §5.1/§7.4 装配 Windows 安装目录 ACL 规则序列：
//
//  1. 根 DataDir：断继承 + SYSTEM F / Administrators F / Users R（递归收口）；
//  2. config\：追加 Users M（GUI 免提权写，含 trigger.json）；
//  3. state\、logs\：重新收口 SYSTEM F / Administrators F / Users R
//     （子目录收口，覆盖父目录 config 继承下来的 M）。
//
// 纯函数（无 OS 调用），任意平台可单测：逐参断言防"整串 argv"回归，并校验
// 权限矩阵（state=R / config=Users M）。子目录用显式 '\' 拼接（与 Windows
// 生产路径形态一致，测试平台无关）。
func windowsACLRules(dataDir string) []icaclsRule {
	return []icaclsRule{
		{dir: dataDir, args: restrictArgs()},
		{dir: dataDir + `\config`, args: usersModifyArgs()},
		{dir: dataDir + `\state`, args: restrictArgs()},
		{dir: dataDir + `\logs`, args: restrictArgs()},
	}
}

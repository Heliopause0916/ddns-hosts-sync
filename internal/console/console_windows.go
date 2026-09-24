//go:build windows

package console

import "golang.org/x/sys/windows"

// DecodeAnsi 按系统 ANSI 代码页（GetACP）把外部命令输出解码为 UTF-8：
// UTF-8 直通、命中代码页映射则转码、其余原样返回（详见 decodeWith）。
// 用途限定为解码外部命令（schtasks/icacls/reg 等）CombinedOutput 的
// 管道输出——该场景走 WriteFile 原始字节、按系统 ANSI 代码页编码，
// 需显式转码后拼入错误信息（冒烟 Bug 2）。
func DecodeAnsi(b []byte) string {
	return decodeWith(windows.GetACP(), b)
}

// WrapStdio 程序自身 CLI 输出（raw UTF-8 直写 os.Stdout/os.Stderr）的
// Windows 终端适配。经核对无需任何运行时动作，实现为显式 no-op：
//
//  1. 真控制台（GetConsoleMode 成功）：Go 标准库对控制台句柄走
//     writeConsole→WriteConsoleW（UTF-16）渲染，与代码页无关，程序
//     自身 UTF-8 输出显示本就正确。此前尝试的 SetConsoleOutputCP(65001)
//     于 GBK 控制台无益，且无版本门控（Win7/8 legacy conhost 对 65001
//     渲染有缺陷）、无恢复原值路径（各子命令以 os.Exit 收尾，defer
//     不可达，改动常驻影响后续命令），已撤销；
//  2. 管道/重定向/服务/计划任务（GetConsoleMode 失败）：走 WriteFile 原
//     字节 UTF-8 直通，此为本程序正确行为，不得改写流；
//  3. 已知限制：唯一残余乱码面是"管道下游按 GBK 等非 UTF-8 代码页解码"
//     的第三方消费者（如 GBK 终端 + more），程序在 tty 侧无法感知；
//     未来如需解决，可在本函数内对会话期的输出流做编码包装
//     （transform.NewWriter，见 console_test.go 的等价直连用法）再解。
func WrapStdio() {}

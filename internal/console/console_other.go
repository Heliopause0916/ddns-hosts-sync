//go:build !windows

package console

// DecodeAnsi 非 Windows 平台：外部命令输出与终端一律按 UTF-8 处理，原样返回。
func DecodeAnsi(b []byte) string { return string(b) }

// WrapStdio 非 Windows 平台：no-op（终端恒为 UTF-8，无需换码），跨平台安全。
func WrapStdio() {}

package tasks

import (
	"os/exec"
	"strings"
	"unicode/utf16"

	"github.com/heliopause/ddns-hosts-sync/internal/console"
)

// ---------------------------------------------------------------------------
// schtasks 纯工具逻辑（M8/S12：无 build tag，任意平台可编译可单测）。
// 原实现位于 tasks_windows.go，多语言判定的回归依赖 Windows 环境无法在
// 跨平台 CI 覆盖，故下沉为本文件并配任意平台单测。
// ---------------------------------------------------------------------------

// schtasksNotFoundMarkers "任务不存在"输出报文标记（大小写不敏感匹配）。
// 覆盖：
//   - 通用：Win32 错误码 0x80070002（ERROR_FILE_NOT_FOUND）；
//   - 英文：The system cannot find the file specified. / does not exist；
//   - 简体中文：错误: 系统找不到指定的文件。 / 错误: 系统找不到指定的路径。
//   - 繁体中文：錯誤: 系統找不到指定的檔案。 / 錯誤: 無法找到指定的檔案。
//   - 无可用任务（/Query 场景的 zh-TW 变体）：沒有可用的...
var schtasksNotFoundMarkers = []string{
	"0x80070002",
	"cannot find the file",
	"does not exist",
	"not found",
	"找不到",
	"指定的檔案",
	"無法找到",
	"没有可用的",
	"沒有可用的",
}

// isTaskNotFound 从 schtasks 退出码/输出识别"任务不存在"（Uninstall/Exists
// 幂等语义依赖）：退出码 1 或 2（schtasks /Query 缺失任务返回 1、
// ERROR_FILE_NOT_FOUND）即判不存在；另按输出报文标记兜底多语言系统。
// 输出先经 console.DecodeAnsi 解码为 UTF-8 再匹配——GBK 环境 schtasks 输出
// 是 GBK 字节，原始字节上"找不到"等标记永不命中，只能靠退出码兜底，
// 解码后中文标记即可正确命中（冒烟 Bug 2；退出码兜底逻辑保留不动）。
func isTaskNotFound(out []byte, err error) bool {
	if ee, ok := err.(*exec.ExitError); ok {
		switch ee.ExitCode() {
		case 1, 2: // 2=经典 ERROR_FILE_NOT_FOUND；英文系统 schtasks 报 1
			return true
		}
	}
	low := strings.ToLower(console.DecodeAnsi(out))
	for _, marker := range schtasksNotFoundMarkers {
		if strings.Contains(low, marker) {
			return true
		}
	}
	return false
}

// utf16EncodeLE 将 UTF-8 字节按 UTF-16LE 编码并加 LE BOM（FF FE）。
// 调用方保证入参为合法 UTF-8（schtasks XML）；非法 rune 由 utf16.Encode
// 以替代符兜底。schtasks 要求 XML 文件为 UTF-16LE（带 BOM）或 ASCII
// （DSD §7.3：路径可能含中文，统一编码）。
func utf16EncodeLE(b []byte) []byte {
	runes := []rune(string(b))
	units := utf16.Encode(runes)
	out := make([]byte, 0, len(units)*2+2)
	out = append(out, 0xFF, 0xFE) // UTF-16LE BOM
	for _, u := range units {
		out = append(out, byte(u&0xFF), byte(u>>8))
	}
	return out
}

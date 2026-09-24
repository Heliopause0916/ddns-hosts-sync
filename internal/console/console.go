// Package console 提供跨平台的最小编码适配层（冒烟 Bug 2）：
//
//   - DecodeAnsi：把外部命令（schtasks/icacls/reg 等）按系统 ANSI 代码页
//     输出的原始字节解码为 UTF-8 字符串，用于错误信息拼装（GBK 环境不再乱码）；
//   - WrapStdio：程序启动时调用；经核对 Windows 真控制台由 WriteConsoleW
//     （UTF-16）渲染、管道/重定向为 UTF-8 直通，均无需运行时动作，故为
//     显式 no-op（详见 console_windows.go 注释的三条机制理由）。
//
// 代码页映射做成与平台无关的纯函数（decodeWith/encodeWith，decode/encode
// 对称），可跨平台单测；Windows 侧仅负责取系统 ANSI 代码页（GetACP）。
// 依赖 golang.org/x/text 与 golang.org/x/sys（go.sum 已有条目，无新增
// 第三方依赖）。
package console

import (
	"unicode/utf8"

	"golang.org/x/text/encoding"
	"golang.org/x/text/encoding/charmap"
	"golang.org/x/text/encoding/japanese"
	"golang.org/x/text/encoding/korean"
	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/encoding/traditionalchinese"
	"golang.org/x/text/transform"
)

// cpEncodings Windows ANSI/控制台代码页 → 编码映射（纯查找表，跨平台可单测）。
// 936/950/932/949 为多字节（DBCS）代码页，按 x/text 映射就位；1250-1258 为
// Windows-125x 单字节代码页（charmap 包同款命名）。未命中映射时返回 false，
// 由调用方按"原样透传"兜底（宁可不过滤，不可二次转码破坏字节）。
var cpEncodings = map[uint32]encoding.Encoding{
	936:  simplifiedchinese.GBK,
	950:  traditionalchinese.Big5,
	932:  japanese.ShiftJIS,
	949:  korean.EUCKR,
	1250: charmap.Windows1250,
	1251: charmap.Windows1251,
	1252: charmap.Windows1252,
	1253: charmap.Windows1253,
	1254: charmap.Windows1254,
	1255: charmap.Windows1255,
	1256: charmap.Windows1256,
	1257: charmap.Windows1257,
	1258: charmap.Windows1258,
}

// decodeWith 按代码页 cp 将原始字节 b 解码为 UTF-8 字符串：
//   - b 为合法 UTF-8（含 cp=65001）时原样返回，保证幂等、杜绝二次解码；
//   - cp 命中映射表时经 x/text 解码器转换；解码失败或未命中映射时原样返回。
func decodeWith(cp uint32, b []byte) string {
	if cp == 65001 || utf8.Valid(b) {
		return string(b)
	}
	enc, ok := cpEncodings[cp]
	if !ok {
		return string(b)
	}
	s, _, err := transform.String(enc.NewDecoder(), string(b))
	if err != nil {
		return string(b)
	}
	return s
}

// encodeWith 将 UTF-8 字节 b 按代码页 cp 编码（与 decodeWith 对称）：
//   - cp=65001 或输入非合法 UTF-8 时原样返回（输入疑似已编码，避免二次转码）；
//   - cp 命中映射表时经 x/text 编码器转换；编码失败或未命中映射时原样返回。
func encodeWith(cp uint32, b []byte) []byte {
	if cp == 65001 || !utf8.Valid(b) {
		return b
	}
	enc, ok := cpEncodings[cp]
	if !ok {
		return b
	}
	out, _, err := transform.Bytes(enc.NewEncoder(), b)
	if err != nil {
		return b
	}
	return out
}

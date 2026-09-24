package console

import (
	"bytes"
	"testing"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// ---------------------------------------------------------------------------
// 纯函数层单测（跨平台，不依赖真控制台；Windows 侧仅取系统 ANSI 代码页
// GetACP，行为已收敛到 decodeWith/encodeWith，此处覆盖）
// ---------------------------------------------------------------------------

// TestDecodeWithGBK GBK 字节（cp=936）应还原为中文；用"编码→解码"精确断言
// 而非同表往返，避免编码/解码表双错抵消的假阳性。
func TestDecodeWithGBK(t *testing.T) {
	src := "错误: 系统找不到指定的文件。 (0x80070002)"
	gbkB, _, err := transform.Bytes(simplifiedchinese.GBK.NewEncoder(), []byte(src))
	if err != nil {
		t.Fatalf("构造 GBK 字节失败: %v", err)
	}
	if got := decodeWith(936, gbkB); got != src {
		t.Errorf("decodeWith(936, gbk) = %q, want %q", got, src)
	}
}

// TestDecodeWithUTF8 cp=65001 与合法 UTF-8 输入一律原样直通（幂等）。
func TestDecodeWithUTF8(t *testing.T) {
	cases := []struct {
		name string
		cp   uint32
		in   []byte
	}{
		{"cp65001", 65001, []byte("ddns-hosts-sync 同步")},
		{"cp936 但输入本身是 UTF-8", 936, []byte("ddns-hosts-sync 同步")},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := decodeWith(c.cp, c.in); got != string(c.in) {
				t.Errorf("decodeWith(%d, %q) = %q, want 原样返回 %q", c.cp, c.in, got, c.in)
			}
		})
	}
}

// TestEncodeDecodeSymmetric encodeWith 与 decodeWith 对称（中文 DBCS + 西文
// 单字节各取一例）。
func TestEncodeDecodeSymmetric(t *testing.T) {
	cases := []struct {
		name string
		cp   uint32
		s    string
	}{
		{"936 简体中文", 936, "ddns-hosts-sync 后台同步"},
		{"1252 西文", 1252, "café résumé"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			enc := encodeWith(c.cp, []byte(c.s))
			if got := decodeWith(c.cp, enc); got != c.s {
				t.Errorf("encode→decode 往返失败: got %q, want %q", got, c.s)
			}
		})
	}
}

// TestEncodeDecodeUnknownCP 未命中映射的代码页：原样透传（不二次转码、不丢弃）。
func TestEncodeDecodeUnknownCP(t *testing.T) {
	in := []byte{0xFF, 0xFE, 0x00, 0x81}
	if got := decodeWith(9999, in); got != string(in) {
		t.Errorf("decodeWith(9999, %x) = %q, want 原样 %q", in, got, in)
	}
	if got := encodeWith(9999, in); !bytes.Equal(got, in) {
		t.Errorf("encodeWith(9999, %x) = %x, want 原样", in, got)
	}
}

// TestEncodeWithUTF8CP cp=65001：编码直通（终端按 UTF-8 渲染）。
func TestEncodeWithUTF8CP(t *testing.T) {
	in := []byte("ddns-hosts-sync 同步")
	if got := encodeWith(65001, in); !bytes.Equal(got, in) {
		t.Errorf("encodeWith(65001, %q) = %x, want 原样", in, got)
	}
}

// TestTransformWriterRoundtrip 流式编码（transform.NewWriter，即未来流包装
// 将采用的机制）：把 UTF-8 中文写入经 GBK 编码包装的 buffer，产物应能经
// decodeWith(936,...) 反向解码还原原文（输出 GBK 字节可被 GBK 解码还原，
// 语义等价于原 newTermWriter 断言，且不依赖包内私有包装）。
func TestTransformWriterRoundtrip(t *testing.T) {
	src := "后台同步"
	var buf bytes.Buffer
	tw := transform.NewWriter(&buf, simplifiedchinese.GBK.NewEncoder())
	if _, err := tw.Write([]byte(src)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	if got := decodeWith(936, buf.Bytes()); got != src {
		t.Errorf("transform.NewWriter 输出经 GBK 解码 = %q, want %q（中间字节 %x）", got, src, buf.Bytes())
	}
}

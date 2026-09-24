package tasks

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// M8/S12：schtasks 纯逻辑跨平台单测（多语言报文 + UTF-16 BOM 字节断言）
// ---------------------------------------------------------------------------

// TestHelperProcessExits 测试辅助子进程：仅在被环境变量点名时以指定退出码
// 退出（跨平台制造真实 *exec.ExitError，exit code 无法直接构造 ProcessState）。
func TestHelperProcessExits(t *testing.T) {
	if os.Getenv("GO_WANT_HELPER_PROCESS") != "1" {
		return
	}
	code, _ := strconv.Atoi(os.Getenv("GO_EXIT_CODE"))
	os.Exit(code)
}

// helperExitErr 运行 helper 子进程制造指定退出码的 *exec.ExitError。
func helperExitErr(t *testing.T, code int) *exec.ExitError {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperProcessExits")
	cmd.Env = append(os.Environ(), "GO_WANT_HELPER_PROCESS=1",
		fmt.Sprintf("GO_EXIT_CODE=%d", code))
	err := cmd.Run()
	ee, ok := err.(*exec.ExitError)
	if !ok {
		t.Fatalf("子进程应以 ExitError 退出，实际 %v", err)
	}
	return ee
}

func TestIsTaskNotFound(t *testing.T) {
	cases := []struct {
		name string
		out  string
		code int // 0 表示 err=nil（无退出码）
		want bool
	}{
		// 退出码路径：英文系统 schtasks /Query 缺失任务退出码 1。
		{"退出码 1（英文系统文件未找到）", "", 1, true},
		{"退出码 2（经典 ERROR_FILE_NOT_FOUND）", "", 2, true},
		// 英文报文。
		{"英文 The system cannot find the file specified.", "ERROR: The system cannot find the file specified.", 1, true},
		{"英文 task name does not exist", "ERROR: The task name \"ddns-hosts-sync\" does not exist.", 1, true},
		{"英文 0x80070002", "ERROR: 0x80070002 The system cannot find the file specified.", 0, true},
		// 简体中文报文。
		{"简体中文 找不到指定的文件", "错误: 系统找不到指定的文件。 (0x80070002)", 0, true},
		{"简体中文 找不到指定的路径", "错误: 系统找不到指定的路径。", 1, true},
		// 繁体中文报文。
		{"繁体中文 系統找不到指定的檔案", "錯誤: 系統找不到指定的檔案。 (0x80070002)", 0, true},
		{"繁体中文 沒有可用的工作", "錯誤: 沒有可用的工作。", 1, true},
		{"繁体中文 無法找到指定的檔案", "錯誤: 無法找到指定的檔案。", 1, true},
		// 非"不存在"类失败：绝不误判。
		{"退出码 5 访问被拒不判不存在", "ERROR: Access is denied.", 5, false},
		{"退出码 0 且无匹配报文不判不存在", "not at all matching", 0, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var err error
			if c.code != 0 {
				err = helperExitErr(t, c.code)
			}
			if got := isTaskNotFound([]byte(c.out), err); got != c.want {
				t.Errorf("isTaskNotFound(out=%q, code=%d) = %v, want %v", c.out, c.code, got, c.want)
			}
		})
	}
}

// TestUTF16EncodeLEBOM UTF-16LE 编码字节断言：BOM 为 FF FE，ASCII 低位在前。
func TestUTF16EncodeLEBOM(t *testing.T) {
	got := utf16EncodeLE([]byte("abc"))
	want := []byte{0xFF, 0xFE, 'a', 0x00, 'b', 0x00, 'c', 0x00}
	if !bytes.Equal(got, want) {
		t.Errorf("utf16EncodeLE(\"abc\") = % x, want % x", got, want)
	}
}

// TestUTF16EncodeLEChinese 中文（多字节 UTF-8）转 UTF-16LE 后小端字节序正确。
func TestUTF16EncodeLEChinese(t *testing.T) {
	got := utf16EncodeLE([]byte("路径中文"))
	if len(got) < 3 || got[0] != 0xFF || got[1] != 0xFE {
		t.Fatalf("应以 FF FE BOM 开头: % x", got[:2])
	}
	// 中文字符 U+8DEF（路）→ UTF-16LE 小端字节 EF 8D。
	if !bytes.Contains(got[2:], []byte{0xEF, 0x8D}) {
		t.Errorf("中文字符应按 UTF-16LE 小端编码（路=U+8DEF → EF 8D）: % x", got)
	}
	// 去除 BOM 后每个字符恰 2 字节（长度偶数）。
	if (len(got)-2)%2 != 0 {
		t.Errorf("去除 BOM 后长度应为偶数（每字符 2 字节）: %d", len(got)-2)
	}
	if strings.HasSuffix(string(got), "\x00\x8d") {
		t.Error("字节序不应为大端")
	}
}

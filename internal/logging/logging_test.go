package logging

import (
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

// TestLogFormat 校验单行格式：`时间 UTC+8 LEVEL [entry_id] msg`、LEVEL 对齐 5、
// 空 entry_id 输出 []。
func TestLogFormat(t *testing.T) {
	path := filepath.Join(t.TempDir(), "sync.log")
	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	defer lg.Close()

	lg.Info("e-01", "resolved 203.0.113.42 chain=a->b used_fallback=false")
	lg.Warn("", "backup failed: boom")
	lg.Error("e-02", "hosts write failed")

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读日志失败: %v", err)
	}
	lines := strings.Split(strings.TrimRight(string(data), "\n"), "\n")
	if len(lines) != 3 {
		t.Fatalf("应 3 行，实际 %d: %q", len(lines), lines)
	}
	tsRe := regexp.MustCompile(`^\d{4}-\d{2}-\d{2}T\d{2}:\d{2}:\d{2}\+08:00 `)
	for i, l := range lines {
		if !tsRe.MatchString(l) {
			t.Errorf("行 %d 时间戳不符（需 UTC+8 RFC3339）: %q", i, l)
		}
	}
	if !strings.Contains(lines[0], " INFO  [e-01] resolved 203.0.113.42") {
		t.Errorf("INFO 行格式不符: %q", lines[0])
	}
	if !strings.Contains(lines[1], " WARN  [] backup failed: boom") {
		t.Errorf("WARN 行格式不符（空 id 应输出 []）: %q", lines[1])
	}
	if !strings.Contains(lines[2], " ERR   [e-02] hosts write failed") {
		t.Errorf("ERR 行格式不符（对齐 5）: %q", lines[2])
	}
}

// TestRotate 校验 >1MB 轮转：旧文件 rename 为 sync.log.1 覆盖，新文件重建。
func TestRotate(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")

	lg, err := Open(path)
	if err != nil {
		t.Fatalf("Open 失败: %v", err)
	}
	pad := strings.Repeat("x", 2048)
	for i := 0; i < 600; i++ { // ~1.2MB
		lg.Info("", pad)
	}
	lg.Close()

	info, _ := os.Stat(path)
	if info.Size() < 1024*1024 {
		t.Skipf("写入量不足（%d），轮转断言不可靠", info.Size())
	}
	lg2, err := Open(path)
	if err != nil {
		t.Fatalf("二次 Open 失败: %v", err)
	}
	defer lg2.Close()

	if _, err := os.Stat(path + ".1"); err != nil {
		t.Fatalf("应存在轮转文件 sync.log.1: %v", err)
	}
	info2, _ := os.Stat(path)
	if info2.Size() > 0 {
		t.Errorf("轮转后主日志应重建为空，实际 %d", info2.Size())
	}
}

// TestNilLoggerNoop nil 接收者（日志不可用降级）静默丢弃不 panic。
func TestNilLoggerNoop(t *testing.T) {
	var lg *Logger
	lg.Info("", "x")
	lg.Warn("", "x")
	lg.Error("", "x")
	if err := lg.Close(); err != nil {
		t.Errorf("nil Close 应返回 nil: %v", err)
	}
}

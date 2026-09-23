package sync

import "testing"

// TestFlushDNSCacheFailureIsReportedButNonFatal flushDNSCache 兜底败路径：
// PATH 中无 resolvectl/systemd-resolve → 返回错误（调用方仅 warn，不阻断写盘）。
func TestFlushDNSCacheFailureIsReportedButNonFatal(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // 空 PATH，任何命令都 LookPath 失败
	if err := flushDNSCache(); err == nil {
		t.Error("无可用缓存刷新命令应返回错误")
	}
}

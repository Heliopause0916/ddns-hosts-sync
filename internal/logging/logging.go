// Package logging 提供同步日志（DSD §5.4）。
//
// 单行文本 `<时间 UTC+8> <LEVEL> [entry_id] <msg>`，LEVEL ∈ {INFO, WARN, ERR}，
// 基于标准库 log 语义扩展，无第三方依赖；ERR 行并行输出到 os.Stderr。
// 轮转：每次 Open 检查文件大小 >1MB → 当前文件 rename 为 `<name>.1`
// （循环覆盖旧版）→ 重建新文件（任务为短生命周期进程，启动时轮转即可）。
package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"
)

// 日志级别（对齐宽度 5：INFO / WARN / ERR + 补空格）。
const (
	LevelInfo = "INFO"
	LevelWarn = "WARN"
	LevelErr  = "ERR"
)

// RotateBytes 单文件轮转阈值：1MB（DSD §5.4）。
const RotateBytes = 1 << 20

// Logger 文件日志句柄（并发安全）。
type Logger struct {
	mu   sync.Mutex
	f    *os.File
	path string
}

// Open 打开（必要时创建）日志文件并执行启动轮转检查。
func Open(path string) (*Logger, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("logging: 创建日志目录: %w", err)
	}
	l := &Logger{path: path}
	if err := l.openRotated(); err != nil {
		return nil, err
	}
	return l, nil
}

// openRotated 检查 >1MB 则轮转并打开 append 句柄。
func (l *Logger) openRotated() error {
	info, err := os.Stat(l.path)
	var size int64
	switch {
	case err == nil:
		size = info.Size()
	case os.IsNotExist(err):
		size = 0
	default:
		return fmt.Errorf("logging: stat %s: %w", l.path, err)
	}
	if size > RotateBytes {
		old := l.path + ".1"
		_ = os.Remove(old) // 循环覆盖旧 sync.log.1
		if rerr := os.Rename(l.path, old); rerr != nil {
			// 轮转失败不致命：继续 append 到原文件（下轮再试）。
			size = info.Size()
		} else {
			size = 0
		}
	}
	f, err := os.OpenFile(l.path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("logging: 打开 %s: %w", l.path, err)
	}
	l.f = f
	return nil
}

// Info 记录 INFO 级日志；entryID 为空输出 []。
func (l *Logger) Info(entryID, msg string) { l.write(LevelInfo, entryID, msg) }

// Warn 记录 WARN 级日志。
func (l *Logger) Warn(entryID, msg string) { l.write(LevelWarn, entryID, msg) }

// Error 记录 ERR 级日志（并行输出到 os.Stderr）。
func (l *Logger) Error(entryID, msg string) { l.write(LevelErr, entryID, msg) }

// Close 关闭日志文件句柄。
func (l *Logger) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	err := l.f.Close()
	l.f = nil
	return err
}

// write 组装单行并以追加方式落盘；ERR 镜像到 stderr。nil 接收者（日志不可用）
// 静默丢弃，不阻断同步主流程。
func (l *Logger) write(level, entryID, msg string) {
	if l == nil || l.f == nil {
		return
	}
	line := fmt.Sprintf("%s %-5s [%s] %s\n", timestamp(), level, entryID, msg)
	l.mu.Lock()
	defer l.mu.Unlock()
	_, _ = l.f.WriteString(line)
	if level == LevelErr {
		_, _ = fmt.Fprint(os.Stderr, line)
	}
}

// timestamp 返回固定 UTC+8 时区的 RFC3339 格式时间（DSD §5.4）。
func timestamp() string {
	return time.Now().In(time.FixedZone("CST", 8*3600)).Format("2006-01-02T15:04:05Z07:00")
}

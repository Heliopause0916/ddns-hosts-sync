package tray

import (
	"errors"
	"fmt"
	"os"
	"time"
)

// InstanceLockStaleAge 陈旧锁自愈阈值：持有进程异常退出（无 Release）后遗留
// 的锁文件超过该时限即视为失效，后到者可接管（DSD §3.4 多实例行 §7.9）。
const InstanceLockStaleAge = 10 * time.Minute

// ErrAlreadyRunning 单实例锁已被其他实例持有（后到者提示"程序已在运行"退出，
// DSD §3.4 托盘多实例行）。
var ErrAlreadyRunning = errors.New("程序已在运行")

// InstanceLock 基于锁文件的 O_EXCL 单实例互斥（跨平台一致，可在 Linux 测试；
// 相比 Windows 命名 Mutex 的取舍见 DSD §7.9 与设计说明）。
type InstanceLock struct {
	path string
	held bool
}

// AcquireInstanceLock 尝试获取单实例锁：
//   - O_CREATE|O_EXCL 创建成功即持有；
//   - 已存在时若锁文件 mtime 超过 staleAge 视为陈旧（崩溃残留），删除后重试一次；
//   - 仍冲突返回 ErrAlreadyRunning。
//
// 锁内容写入持有者 pid，便于排查残留归属。
func AcquireInstanceLock(path string, staleAge time.Duration) (*InstanceLock, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0o644)
	if err == nil {
		_, _ = fmt.Fprintf(f, "%d\n", os.Getpid())
		_ = f.Close()
		return &InstanceLock{path: path, held: true}, nil
	}
	if !os.IsExist(err) {
		return nil, fmt.Errorf("tray: 创建单实例锁 %s: %w", path, err)
	}
	// 已有锁：陈旧自愈。
	if info, statErr := os.Stat(path); statErr == nil && time.Since(info.ModTime()) > staleAge {
		if rmErr := os.Remove(path); rmErr == nil {
			return AcquireInstanceLock(path, staleAge) // 重试一次
		}
	}
	return nil, ErrAlreadyRunning
}

// Release 释放单实例锁（关闭句柄并删除锁文件）。删除失败静默忽略：
// 下次获取时由陈旧自愈兜底，不阻断主流程。
func (l *InstanceLock) Release() {
	if l == nil || !l.held {
		return
	}
	l.held = false
	_ = os.Remove(l.path)
}

// Path 返回锁文件路径（诊断用）。
func (l *InstanceLock) Path() string {
	if l == nil {
		return ""
	}
	return l.path
}

package tray

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestAcquireInstanceLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tray.lock")

	l1, err := AcquireInstanceLock(path, InstanceLockStaleAge)
	if err != nil {
		t.Fatalf("首次获取失败: %v", err)
	}

	t.Run("双锁冲突 → ErrAlreadyRunning", func(t *testing.T) {
		_, err := AcquireInstanceLock(path, time.Hour)
		if !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("第二次获取应报 ErrAlreadyRunning，得 %v", err)
		}
	})

	t.Run("持有者释放后可再获取", func(t *testing.T) {
		l1.Release()
		l2, err := AcquireInstanceLock(path, time.Hour)
		if err != nil {
			t.Fatalf("释放后应可再获取: %v", err)
		}
		l2.Release()
	})

	t.Run("陈旧锁自愈（mtime 超期）", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("12345\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		old := time.Now().Add(-InstanceLockStaleAge - time.Minute)
		if err := os.Chtimes(path, old, old); err != nil {
			t.Fatal(err)
		}
		l3, err := AcquireInstanceLock(path, InstanceLockStaleAge)
		if err != nil {
			t.Fatalf("陈旧锁应自愈接管: %v", err)
		}
		defer l3.Release()
		if l3.Path() != path {
			t.Fatalf("锁路径不符: %s", l3.Path())
		}
	})

	t.Run("新鲜锁不误判陈旧", func(t *testing.T) {
		if err := os.WriteFile(path, []byte("999\n"), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := AcquireInstanceLock(path, time.Hour); !errors.Is(err, ErrAlreadyRunning) {
			t.Fatalf("新鲜锁应仍冲突，得 %v", err)
		}
		_ = os.Remove(path) // 清理模拟锁，避免影响后续子测试
	})

	t.Run("Release 幂等", func(t *testing.T) {
		l4, err := AcquireInstanceLock(path, time.Hour)
		if err != nil {
			t.Fatal(err)
		}
		l4.Release()
		l4.Release() // 二次释放不得 panic/错误
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("释放后锁文件应删除，得 %v", err)
		}
	})

	t.Run("nil 接收者安全", func(t *testing.T) {
		var l *InstanceLock
		l.Release()
	})
}

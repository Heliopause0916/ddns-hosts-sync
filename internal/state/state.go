// Package state 提供状态/触发协议文件的原子读写与消费语义（DSD §2.4、§5.1）。
//
// 原子写约定：`<name>.<pid>.<nanotime>.tmp` 写入同目录 → fsync → rename 覆盖。
// 消费约定：trigger 由任务 rename 为 `trigger.consumed.<requestID>.json` 后删除，
// 恰好消费一次。
package state

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// AtomicWriteJSON 将 v 序列化为 JSON 并以原子写落盘。
func AtomicWriteJSON(path string, v any) error {
	data, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return AtomicWriteFile(path, data)
}

// AtomicWriteFile 以 `<name>.<pid>.<nanotime>.tmp` 写入同目录 → fsync → rename 覆盖。
func AtomicWriteFile(path string, data []byte) error {
	tmp := filepath.Join(filepath.Dir(path),
		fmt.Sprintf("%s.%d.%d.tmp", filepath.Base(path), os.Getpid(), time.Now().UnixNano()))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return err
	}
	_, werr := f.Write(data)
	serr := f.Sync()
	cerr := f.Close()
	if werr != nil || serr != nil || cerr != nil {
		_ = os.Remove(tmp)
		if werr != nil {
			return werr
		}
		if serr != nil {
			return serr
		}
		return cerr
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return err
	}
	syncDir(filepath.Dir(path)) // rename 后父目录 fsync（POSIX 持久性）
	return nil
}

// syncDir 对目录做 fsync 保证 rename 产生的目录项变更落盘（POSIX 持久性）。
// Windows 无法 open 目录，忽略该步（尽力而为，错误不上报）。
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// ReadJSON 从 path 读取并反序列化为 T。
// bool=false 表示文件不存在（此时返回零值 T 与 nil error）。
// 反序列化失败返回 error。
func ReadJSON[T any](path string) (T, bool, error) {
	var zero T
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return zero, false, nil
		}
		return zero, false, err
	}
	if err := json.Unmarshal(data, &zero); err != nil {
		return zero, false, err
	}
	return zero, true, nil
}

// ---------------------------------------------------------------------------
// 状态文件（唯一写者=任务；GUI 轮询读，容忍 rename 窗口内瞬时失败）
// ---------------------------------------------------------------------------

// WriteStatus 原子写 status.json。
func WriteStatus(path string, s *model.SyncStatus) error {
	return AtomicWriteJSON(path, s)
}

// ReadStatus 读取 status.json；bool=false 表示文件不存在。
func ReadStatus(path string) (*model.SyncStatus, bool, error) {
	return ReadJSON[*model.SyncStatus](path)
}

// ---------------------------------------------------------------------------
// 触发文件（唯一写者=GUI；唯一消费者=任务）
// ---------------------------------------------------------------------------

// WriteTrigger 原子写 trigger.json。
func WriteTrigger(path string, t *model.Trigger) error {
	return AtomicWriteJSON(path, t)
}

// removeEntry 归档清理的间接层：包变量便于单测注入"归档删除失败"场景；
// 生产实现恒为 os.Remove。
var removeEntry = os.Remove

// ConsumeTrigger 消费 trigger.json：读到有效文件后 rename 为
// `trigger.consumed.<requestID>.json` 再删除，恰好消费一次。
//
// 返回 bool=true 表示消费到有效文件；文件不存在返回 (nil, false, nil)。
// 损坏/不可解析的 trigger 同样被消费（直接删除），避免每周期重复告警；
// request_id 非法（含路径分隔符等不可用作文件名，DSD §5.1 归档命名）
// 视为损坏处理。消费后的归档删除失败也视为已消费（rename 已发生，不会
// 二次消费），但以非 nil error 上报。新鲜度判定（IsFresh）由调用方完成。
func ConsumeTrigger(path string) (*model.Trigger, bool, error) {
	t, ok, err := ReadJSON[*model.Trigger](path)
	if err != nil {
		// 内容损坏：同样消费（删除），避免每周期重复告警。
		if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
			return nil, false, fmt.Errorf("trigger 内容损坏且清理失败: %v (解析错误: %w)", rerr, err)
		}
		return nil, false, err
	}
	if !ok {
		return nil, false, nil
	}
	if !validRequestID(t.RequestID) {
		// 非法 request_id 不进归档文件名（防路径穿越），直接删除。
		if rerr := os.Remove(path); rerr != nil && !os.IsNotExist(rerr) {
			return nil, false, fmt.Errorf("trigger request_id 非法且清理失败: %v", rerr)
		}
		return nil, false, fmt.Errorf("trigger request_id 非法（%q），已直接删除", t.RequestID)
	}
	base := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	archived := filepath.Join(filepath.Dir(path), fmt.Sprintf("%s.consumed.%s.json", base, t.RequestID))
	if err := os.Rename(path, archived); err != nil {
		return nil, false, err
	}
	if err := removeEntry(archived); err != nil {
		// rename 已发生，消费成功；仅归档清理失败。
		return t, true, err
	}
	return t, true, nil
}

// validRequestID 判定 request_id 可用于归档文件名：仅 [A-Za-z0-9-]（uuid-like）。
// 空、超长、含路径分隔符或其它字符一律拒绝（按损坏处理直接删除）。
func validRequestID(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for i := 0; i < len(id); i++ {
		c := id[i]
		if !((c >= 'a' && c <= 'z') || (c >= 'A' && c <= 'Z') || (c >= '0' && c <= '9') || c == '-') {
			return false
		}
	}
	return true
}

// IsFresh 判定触发新鲜度：requested_at 距今 ≤ maxAge（调用方传 2*time.Minute）。
// 空触发或零值时间戳视为不新鲜；未来时间戳（时钟偏移）视为新鲜。
func IsFresh(t *model.Trigger, now time.Time, maxAge time.Duration) bool {
	if t == nil || t.RequestedAt.IsZero() {
		return false
	}
	return now.Sub(t.RequestedAt) <= maxAge
}

// ---------------------------------------------------------------------------
// 崩溃残留自愈：清理 state 目录中 mtime 超过 maxAge 的 *.tmp（DSD §5.1，启动时 1h）
// ---------------------------------------------------------------------------

// CleanupStaleTmp 删除 dir 下修改时间早于 maxAge 的 *.tmp 残留文件，
// 返回成功删除的文件数。目录不可读返回 error。
func CleanupStaleTmp(dir string, maxAge time.Duration) (int, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return 0, err
	}
	removed := 0
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".tmp") {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		if time.Since(info.ModTime()) > maxAge {
			if err := os.Remove(filepath.Join(dir, e.Name())); err == nil {
				removed++
			}
		}
	}
	return removed, nil
}

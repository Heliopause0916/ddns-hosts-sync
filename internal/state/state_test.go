package state

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// TestAtomicWriteJSONRoundTrip AtomicWriteJSON 写 → ReadJSON 读一致性。
func TestAtomicWriteJSONRoundTrip(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")

	want := &model.SyncStatus{
		Version:         1,
		UpdatedAt:       time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC),
		NextScheduledAt: time.Date(2026, 9, 24, 10, 5, 0, 0, time.UTC),
		IntervalMinutes: 5,
		SyncWindow:      model.WindowSynced,
		Task: model.TaskStatus{
			LastRunAt: time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC),
			LastRunOK: true,
		},
		Entries: []model.EntryStatus{
			{
				ID:                 "e1",
				Status:             model.EntryOK,
				ResolvedIPs:        []string{"203.0.113.42"},
				ResolvedCNAMEChain: []string{"src.example.com", "relay.example.net"},
				UsedFallback:       true,
				HostsPresent:       true,
			},
		},
		HostsBlock: model.HostsBlockStatus{
			Present:     true,
			ContentMD5:  "abc",
			LastWriteOK: true,
		},
	}

	if err := AtomicWriteJSON(path, want); err != nil {
		t.Fatalf("AtomicWriteJSON: %v", err)
	}

	got, ok, err := ReadJSON[*model.SyncStatus](path)
	if err != nil || !ok {
		t.Fatalf("ReadJSON: ok=%v err=%v", ok, err)
	}
	if got.IntervalMinutes != want.IntervalMinutes ||
		got.SyncWindow != want.SyncWindow ||
		!got.Task.LastRunOK ||
		len(got.Entries) != 1 ||
		got.Entries[0].Status != model.EntryOK ||
		got.Entries[0].ResolvedIPs[0] != "203.0.113.42" {
		t.Errorf("读回内容与写入不一致: got %+v", got)
	}
	if got.HostsBlock.ContentMD5 != "abc" {
		t.Errorf("HostsBlock.ContentMD5 不一致: %q", got.HostsBlock.ContentMD5)
	}
	if !got.UpdatedAt.Equal(want.UpdatedAt) {
		t.Errorf("UpdatedAt 不一致: %v != %v", got.UpdatedAt, want.UpdatedAt)
	}
}

// TestAtomicWriteFileOverwrite 同一路径二次写入应覆盖，且无 .tmp 残留。
func TestAtomicWriteFileOverwrite(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trigger.json")

	if err := AtomicWriteFile(path, []byte("first")); err != nil {
		t.Fatalf("第一次写入: %v", err)
	}
	if err := AtomicWriteFile(path, []byte("second")); err != nil {
		t.Fatalf("第二次写入: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(data) != "second" {
		t.Errorf("内容 = %q, 期望 second", data)
	}
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("残留 tmp 文件: %s", e.Name())
		}
	}
}

// TestReadJSONMissing 文件不存在返回 (zero, false, nil)，非 nil error。
func TestReadJSONMissing(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nope.json")
	s, ok, err := ReadJSON[*model.SyncStatus](path)
	if err != nil {
		t.Fatalf("文件不存在应返回 nil err, got %v", err)
	}
	if ok {
		t.Error("文件不存在 ok 应为 false")
	}
	if s != nil {
		t.Errorf("文件不存在应返回 nil 值, got %+v", s)
	}
}

// TestReadJSONInvalid 文件存在但 JSON 非法应返回错误。
func TestReadJSONInvalid(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	if err := os.WriteFile(path, []byte("{not json"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, _, err := ReadJSON[*model.SyncStatus](path); err == nil {
		t.Error("非法 JSON 应返回错误")
	}
}

// TestWriteReadStatus WriteStatus → ReadStatus 往返。
func TestWriteReadStatus(t *testing.T) {
	path := filepath.Join(t.TempDir(), "status.json")
	want := &model.SyncStatus{
		Version:    1,
		SyncWindow: model.WindowWaiting,
		Entries:    []model.EntryStatus{},
		Task:       model.TaskStatus{LastRunOK: true},
	}
	if err := WriteStatus(path, want); err != nil {
		t.Fatalf("WriteStatus: %v", err)
	}
	got, ok, err := ReadStatus(path)
	if err != nil || !ok {
		t.Fatalf("ReadStatus: ok=%v err=%v", ok, err)
	}
	if got.SyncWindow != model.WindowWaiting || !got.Task.LastRunOK {
		t.Errorf("读回不一致: %+v", got)
	}
}

// TestConsumeTriggerExactlyOnce 触发文件消费恰好一次，二次消费返回 false；
// 归档文件在删除后不残留。
func TestConsumeTriggerExactlyOnce(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "trigger.json")

	want := &model.Trigger{
		Version:     1,
		RequestID:   "uuid-123",
		RequestedAt: time.Now().UTC(),
		Action:      model.ActionSyncNow,
	}
	if err := WriteTrigger(path, want); err != nil {
		t.Fatalf("WriteTrigger: %v", err)
	}

	// 第一次消费
	got, consumed, err := ConsumeTrigger(path)
	if err != nil {
		t.Fatalf("首次消费: %v", err)
	}
	if !consumed {
		t.Fatal("首次消费应返回 consumed=true")
	}
	if got == nil || got.RequestID != "uuid-123" || got.Action != model.ActionSyncNow {
		t.Errorf("消费内容不符: %+v", got)
	}

	// 二次消费：触发文件已被 rename+删除，返回 false
	_, consumed2, err := ConsumeTrigger(path)
	if err != nil {
		t.Fatalf("二次消费: %v", err)
	}
	if consumed2 {
		t.Error("二次消费应返回 consumed=false（恰好消费一次）")
	}

	// 目录中不应残留 trigger.json 或归档文件
	entries, _ := os.ReadDir(dir)
	if len(entries) != 0 {
		names := make([]string, 0, len(entries))
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Errorf("消费后目录应清空, 仍存在: %v", names)
	}
}

// TestConsumeTriggerMissing 无触发文件返回 (nil, false, nil)。
func TestConsumeTriggerMissing(t *testing.T) {
	_, consumed, err := ConsumeTrigger(filepath.Join(t.TempDir(), "trigger.json"))
	if err != nil {
		t.Fatalf("无触发文件应返回 nil err, got %v", err)
	}
	if consumed {
		t.Error("无触发文件 consumed 应为 false")
	}
}

// TestIsFreshBoundary IsFresh 边界：
//   - age == maxAge → 新鲜（≤）
//   - age > maxAge → 不新鲜
//   - 未来时间戳（时钟偏移）→ 新鲜
//   - 零值/空触发 → 不新鲜
func TestIsFreshBoundary(t *testing.T) {
	now := time.Now().UTC()
	maxAge := 2 * time.Minute

	// 恰好 maxAge
	atEdge := &model.Trigger{Version: 1, RequestID: "r1", RequestedAt: now.Add(-maxAge)}
	if !IsFresh(atEdge, now, maxAge) {
		t.Error("age == maxAge 应视为新鲜（≤ 语义）")
	}

	// 超过 maxAge
	stale := &model.Trigger{Version: 1, RequestID: "r2", RequestedAt: now.Add(-maxAge - 1*time.Nanosecond)}
	if IsFresh(stale, now, maxAge) {
		t.Error("age > maxAge 应视为过期")
	}

	// 远早于 maxAge
	veryStale := &model.Trigger{Version: 1, RequestID: "r3", RequestedAt: now.Add(-time.Hour)}
	if IsFresh(veryStale, now, maxAge) {
		t.Error("1 小时前的触发应视为过期")
	}

	// 未来时间戳
	future := &model.Trigger{Version: 1, RequestID: "r4", RequestedAt: now.Add(5 * time.Minute)}
	if !IsFresh(future, now, maxAge) {
		t.Error("未来时间戳（时钟偏移）应视为新鲜")
	}

	// 零值时间戳
	zero := &model.Trigger{Version: 1, RequestID: "r5"}
	if IsFresh(zero, now, maxAge) {
		t.Error("零值 RequestedAt 应视为不新鲜")
	}

	// nil
	if IsFresh(nil, now, maxAge) {
		t.Error("nil 触发应视为不新鲜")
	}
}

// TestCleanupStaleTmp 构造 mtime>1h 与 <1h 的 *.tmp，断言只清掉旧的；
// 非 tmp 文件、子目录不受影响。
func TestCleanupStaleTmp(t *testing.T) {
	dir := t.TempDir()
	now := time.Now()

	oldTmp := filepath.Join(dir, "status.json.1234.111111111.tmp")
	newTmp := filepath.Join(dir, "trigger.json.9999.222222222.tmp")
	keepFile := filepath.Join(dir, "status.json")
	if err := os.WriteFile(oldTmp, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(newTmp, nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(keepFile, []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	subdir := filepath.Join(dir, "sub")
	if err := os.Mkdir(subdir, 0o755); err != nil {
		t.Fatal(err)
	}
	// 设定 mtime：oldTmp 2h 前，其余 30 分钟前
	old := now.Add(-2 * time.Hour)
	recent := now.Add(-30 * time.Minute)
	if err := os.Chtimes(oldTmp, old, old); err != nil {
		t.Fatal(err)
	}
	for _, p := range []string{newTmp, keepFile, subdir} {
		if err := os.Chtimes(p, recent, recent); err != nil {
			t.Fatal(err)
		}
	}

	removed, err := CleanupStaleTmp(dir, time.Hour)
	if err != nil {
		t.Fatalf("CleanupStaleTmp: %v", err)
	}
	if removed != 1 {
		t.Fatalf("应清理 1 个过期 tmp, 实际 %d", removed)
	}

	if _, err := os.Stat(oldTmp); !os.IsNotExist(err) {
		t.Error("过期 tmp 应已被删除")
	}
	if _, err := os.Stat(newTmp); err != nil {
		t.Error("新鲜 tmp 不应被删除")
	}
	if _, err := os.Stat(keepFile); err != nil {
		t.Error("非 tmp 文件不应被删除")
	}
	if _, err := os.Stat(subdir); err != nil {
		t.Error("子目录不应被删除")
	}
}

// TestCleanupStaleTmpMissingDir 目录不存在应返回 error。
func TestCleanupStaleTmpMissingDir(t *testing.T) {
	if _, err := CleanupStaleTmp(filepath.Join(t.TempDir(), "no-such-dir"), time.Hour); err == nil {
		t.Error("目录不存在应返回错误")
	}
}

package gui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
	"github.com/heliopause/ddns-hosts-sync/internal/state"
)

func TestStatusWatcherMtimeTrigger(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	w := newStatusWatcher(path)

	// 文件不存在：未变化。
	if _, changed, err := w.poll(); err != nil || changed {
		t.Fatalf("不存在应未变化: changed=%v err=%v", changed, err)
	}

	st := &model.SyncStatus{
		Version:         model.StatusVersion,
		UpdatedAt:       time.Now().UTC(),
		IntervalMinutes: 5,
	}
	if err := state.WriteStatus(path, st); err != nil {
		t.Fatal(err)
	}

	// 首次读到：changed=true。
	got, changed, err := w.poll()
	if err != nil || !changed {
		t.Fatalf("首次读取应 changed: %v %v", changed, err)
	}
	if got == nil || got.IntervalMinutes != 5 {
		t.Fatalf("解析结果不符: %+v", got)
	}

	// mtime/size 未变：changed=false。
	if _, changed, _ := w.poll(); changed {
		t.Fatal("mtime 未变不应重解析")
	}

	// 内容更新（updated_at 前进 → size 变化）→ changed=true。
	st.UpdatedAt = time.Now().UTC().Add(time.Second)
	if err := state.WriteStatus(path, st); err != nil {
		t.Fatal(err)
	}
	if _, changed, _ := w.poll(); !changed {
		t.Fatal("内容变化应重解析")
	}

	// 文件被移除：changed=true（状态丢失通知上层）。
	if err := os.Remove(path); err != nil {
		t.Fatal(err)
	}
	got2, changed, err := w.poll()
	if err != nil || !changed || got2 != nil {
		t.Fatalf("文件移除应 changed 且返回 nil: %v %v %v", got2, changed, err)
	}
}

func TestStatusWatcherRenameRetry(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	w := newStatusWatcher(path)

	// 场景 1：瞬时损坏（rename 窗口读到半截文件）→ 重试一次后仍失败 → err。
	if err := os.WriteFile(path, []byte(`{"version": 1, "updated_at": "broken`), 0o644); err != nil {
		t.Fatal(err)
	}
	_, _, err := w.poll()
	if err == nil {
		t.Fatal("损坏 JSON 应报错（重试后仍失败）")
	}

	// 场景 2：先损坏再变有效（rename 完成 → 重试成功）。
	if err := os.WriteFile(path, []byte(`not json`), 0o644); err != nil {
		t.Fatal(err)
	}
	// 用真实状态文件覆盖（WriteStatus 原子写）模拟 rename 完成。
	st := &model.SyncStatus{
		Version:         model.StatusVersion,
		UpdatedAt:       time.Now().UTC(),
		IntervalMinutes: 5,
		Task:            model.TaskStatus{LastRunOK: true},
	}
	if err := state.WriteStatus(path, st); err != nil {
		t.Fatal(err)
	}
	got, changed, err := w.poll()
	if err != nil {
		t.Fatalf("重试后应成功解析: %v", err)
	}
	if !changed || got == nil || got.IntervalMinutes != 5 {
		t.Fatalf("重试解析结果不符: %+v changed=%v", got, changed)
	}
}

func TestStatusWatcherCachedValueReturned(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "status.json")
	w := newStatusWatcher(path)

	st := &model.SyncStatus{Version: model.StatusVersion, UpdatedAt: time.Now().UTC()}
	if err := state.WriteStatus(path, st); err != nil {
		t.Fatal(err)
	}
	if _, _, err := w.poll(); err != nil {
		t.Fatal(err)
	}
	// 再次 poll（未变）：返回缓存的 st（changed=false）。
	got, changed, err := w.poll()
	if err != nil || changed {
		t.Fatalf("未变时应返回缓存: changed=%v err=%v", changed, err)
	}
	if got == nil || got.Version != model.StatusVersion {
		t.Fatalf("缓存内容不符: %+v", got)
	}
}

func TestReadLogTail(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "sync.log")

	// 不存在 → 空。
	lines, err := readLogTail(path, 200)
	if err != nil || len(lines) != 0 {
		t.Fatalf("不存在应空: %v %v", lines, err)
	}

	// 正常多行（末行无换行符 + 空行剔除）。
	content := "line1\nline2\r\nline3\nline4\n\n"
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, err = readLogTail(path, 200)
	if err != nil {
		t.Fatal(err)
	}
	if len(lines) != 4 || lines[0] != "line1" || lines[3] != "line4" {
		t.Fatalf("行解析不符: %v", lines)
	}

	// 超 200 行截尾。
	var b strings.Builder
	for i := 0; i < 250; i++ {
		b.WriteString("x" + strings.Repeat("a", 10) + "\n")
	}
	if err := os.WriteFile(path, []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
	lines, _ = readLogTail(path, 200)
	if len(lines) != 200 {
		t.Fatalf("应截尾至 200 行，得 %d", len(lines))
	}
}

func TestIndexStatus(t *testing.T) {
	st := &model.SyncStatus{Entries: []model.EntryStatus{
		{ID: "a", Status: model.EntryOK},
		{ID: "b", Status: model.EntryTimeout},
	}}
	m := indexStatus(st)
	if m["a"].Status != model.EntryOK || m["b"].Status != model.EntryTimeout {
		t.Fatalf("indexStatus 结果不符: %+v", m)
	}
	if _, ok := m["c"]; ok {
		t.Fatal("不应含未知 id")
	}
}

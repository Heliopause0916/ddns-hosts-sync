package gui

import (
	"fmt"
	"sync"
	"time"

	"fyne.io/fyne/v2"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
	"github.com/heliopause/ddns-hosts-sync/internal/state"
)

// 立即同步时序参数（DSD §4.5）。
const (
	syncCooldown   = 10 * time.Second // 按钮防连点
	syncNoResponse = 3 * time.Minute  // 无响应判超时
)

// onSyncNow 立即同步（DSD §4.5 步骤 1-4）：
// 1. 生成 trigger（写 state/trigger.json，原子写）；
// 2. 注入的触发函数尽力拉起计划任务（失败静默，兜底下一分钟窗口）；
// 3. 提示 + 按钮 10s 防连点；
// 4. 轮询 updated_at 前进由 updateSyncFeedback 反馈（≤3 分钟无响应红横幅）。
func (g *GUI) onSyncNow() {
	now := time.Now()
	if now.Before(g.syncCooldownUntil) {
		g.showBanner("已请求过，请稍候再试", bannerWarn)
		return
	}

	tg := &model.Trigger{
		Version:     model.StatusVersion,
		RequestID:   newTriggerID(),
		RequestedAt: now.UTC(),
		Action:      model.ActionSyncNow,
	}
	if err := state.WriteTrigger(g.deps.triggerPath(), tg); err != nil {
		g.showBanner("写入立即同步请求失败: "+err.Error(), bannerErr)
		return
	}
	if g.deps.TriggerNow != nil {
		g.deps.TriggerNow() // 尽力触发，失败静默
	}

	g.mu.Lock()
	g.syncReqPending = true
	g.syncReqAt = now
	if g.st != nil {
		g.syncReqUpdatedAt = g.st.UpdatedAt
	}
	g.mu.Unlock()
	g.showBanner("已请求立即同步，最迟 1 分钟完成", bannerOK)

	// 10s 防连点（期间按钮禁用；恢复后勾选态不变）。
	g.syncCooldownUntil = now.Add(syncCooldown)
	if g.syncBtn != nil {
		g.syncBtn.Disable()
		time.AfterFunc(syncCooldown, func() {
			fyne.Do(func() {
				if g.syncBtn != nil {
					g.syncBtn.Enable()
				}
			})
		})
	}
}

// updateSyncFeedback 轮询后评估立即同步结果（DSD §4.5 步骤 6）：
//   - status.updated_at 前进 → 横幅"同步完成"（绿），清除 pending；
//   - 超 3 分钟无前进 → 横幅"后台任务未响应"（红），清除 pending。
//
// 纯逻辑抽离为 syncFeedbackState 便于单测；本方法仅应用 UI 副作用。
func (g *GUI) updateSyncFeedback(stUpdatedAt time.Time) {
	g.mu.Lock()
	if !g.syncReqPending {
		g.mu.Unlock()
		return
	}
	reqAt := g.syncReqAt
	base := g.syncReqUpdatedAt
	g.mu.Unlock()

	done, msg, kind := syncFeedbackState(stUpdatedAt, base, reqAt, time.Now())
	if !done {
		return
	}
	g.mu.Lock()
	g.syncReqPending = false
	g.mu.Unlock()
	g.showBanner(msg, kind)
}

// syncFeedbackState 纯函数：依据最新 updated_at 与请求基线评估立即同步反馈。
//   - 前进 → (true, "同步完成", OK)
//   - 静止但未超时 → (false, "", 0)
//   - 超时 → (true, "后台任务未响应（计划任务可能未安装）", Err)
func syncFeedbackState(stUpdatedAt, base, reqAt, now time.Time) (done bool, msg string, kind bannerKind) {
	if stUpdatedAt.After(base) {
		return true, "同步完成", bannerOK
	}
	if now.Sub(reqAt) > syncNoResponse {
		return true, "后台任务未响应（计划任务可能未安装）", bannerErr
	}
	return false, "", 0
}

// newTriggerID 立即同步 request_id（满足 state 消费归档命名 [A-Za-z0-9-]）。
func newTriggerID() string {
	triggerSeqMu.Lock()
	triggerSeq++
	n := triggerSeq
	triggerSeqMu.Unlock()
	return fmt.Sprintf("gui-%d-%d", time.Now().UnixNano(), n)
}

// 立即同步 request_id 进程内序号（防同名并发）。
var (
	triggerSeq   int64
	triggerSeqMu sync.Mutex
)

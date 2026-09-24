package tray

import (
	"fmt"
	"os"
	"sync/atomic"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// trigger 序号：保证同一进程内多次立即同步的 request_id 不重复。
var triggerSeq atomic.Uint64

// newSyncNowTrigger 构造"立即同步"触发请求（DSD §1.3/§4.5 步骤 1-2）。
// request_id 满足 state 消费归档命名约束 [A-Za-z0-9-]。
// 返回 ok=false 表示构造失败（本函数一般不失败，保留错误通道给调用方）。
func newSyncNowTrigger(now time.Time) (*model.Trigger, bool) {
	id := fmt.Sprintf("tray-%d-%d-%d", os.Getpid(), now.UnixNano(), triggerSeq.Add(1))
	return &model.Trigger{
		Version:     model.StatusVersion,
		RequestID:   id,
		RequestedAt: now.UTC(),
		Action:      model.ActionSyncNow,
	}, true
}

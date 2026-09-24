// Package sync 实现同步编排状态机（DSD §2.3），无 GUI 依赖，为 sync 子命令
// 与 M2 计划任务共用的单次循环入口。
//
// Run 状态机（步骤 1-8）：初始化 → 日志 → 读 config（失败兜底默认 + last_error）
// → 消费 trigger → 间隔门（waiting 最小 status、exit 0）→ 完整同步（解析/装配/
// 变化检测/写盘/flushdns）→ 写 status.json + 日志摘要 → 统一 exit 0（DNS/写盘
// 失败编码进 status，非零退出码仅用于 state 目录不可写等致命错误）。
package sync

import (
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/hostsfile"
	"github.com/heliopause/ddns-hosts-sync/internal/logging"
	"github.com/heliopause/ddns-hosts-sync/internal/model"
	"github.com/heliopause/ddns-hosts-sync/internal/resolver"
	"github.com/heliopause/ddns-hosts-sync/internal/state"
)

// Options Run 的入参（DSD §2.3）。
type Options struct {
	ConfigPath string
	StateDir   string // status.json / trigger.json 所在目录
	LogPath    string
	HostsPath  string
	Force      bool // 忽略间隔门，立即完整同步（trigger sync_now 置 true）
	// IntervalTick 任务注册粒度（固定 60s）。间隔门判据为 lastRunAt+interval，
	// M1 不直接用该值，保留字段供 M2 调度语义使用。
	IntervalTick int
	// Resolve 为 test-only 注入钩子：nil 时使用 resolver.Resolve。
	// 单测借此控制解析结果，实现"变化→写盘、无变化→不写盘、失败→保留"三路断言。
	Resolve func(source string, cfg model.DNSConfig) (*resolver.ResolveResult, error)
	// FlushDNS 系统 DNS 缓存刷新（M2b-1 起由 platform 注入，三平台实现：
	// Windows ipconfig /flushdns、macOS dscacheutil、Linux resolvectl）。
	// nil 时跳过（测试/未接入场景）；调用方保证与 cfg.FlushDNS 联判。
	FlushDNS func() error
}

// triggerMaxAge 触发新鲜度阈值（DSD §1.3：≤2 分钟视为有效）。
const triggerMaxAge = 2 * time.Minute

// 单实例锁参数（DSD §3.4）：冲突等待上限 5s（拿不到 exit 0）；锁文件 mtime
// 超过 10 分钟视为陈旧（进程必然已死，O_EXCL 死锁自愈语义），删除后重试。
// 包变量便于内部单测缩短等待。
var (
	lockWait       = 5 * time.Second
	lockStaleAfter = 10 * time.Minute
)

// acquireSyncLock 以 O_CREATE|O_EXCL 获取 state/sync.lock（DSD §3.4）。
//   - 成功：返回 release 函数（关闭句柄 + 删除锁文件），contended=false；
//   - 冲突：等待 wait 时长，期间若锁变陈旧（mtime > staleAfter）删除重试；
//     等待期满仍拿不到 → contended=true（调用方记日志并 exit 0，不写 status）；
//   - 非 EEXIST 的创建错误（目录只读等）→ 返回非 nil error（致命）。
func acquireSyncLock(path string, wait, staleAfter time.Duration) (release func(), contended bool, err error) {
	deadline := time.Now().Add(wait)
	for {
		f, oerr := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
		if oerr == nil {
			return func() {
				_ = f.Close()
				_ = os.Remove(path)
			}, false, nil
		}
		if !errors.Is(oerr, os.ErrExist) {
			return nil, false, fmt.Errorf("sync.lock 创建失败: %w", oerr)
		}
		if info, serr := os.Stat(path); serr == nil && time.Since(info.ModTime()) > staleAfter {
			if rerr := os.Remove(path); rerr == nil {
				continue // 陈旧锁已清除，下一轮重试创建
			}
		}
		if time.Now().After(deadline) {
			// contended：调用方不得调用 release；返回 no-op 防止误用 panic。
			return func() {}, true, nil
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// Run 执行一次完整循环（sync 子命令入口）。返回最终落盘 status 快照；
// 非 nil error 表示程序自身致命错误（状态目录不可用等），调用方应退出非零。
// 特例：单实例锁冲突等待期满返回 (nil, nil)（DSD §3.4：不写 status，调用方
// 按 exit 0 退出）。
func Run(opts Options) (final *model.SyncStatus, err error) {
	now := time.Now().UTC()

	// ── 1. 初始化：定位状态/触发文件，状态目录必须可用（唯一致命项）。──
	statusPath := filepath.Join(opts.StateDir, "status.json")
	triggerPath := filepath.Join(opts.StateDir, "trigger.json")
	if mkerr := os.MkdirAll(opts.StateDir, 0o755); mkerr != nil {
		return nil, fmt.Errorf("状态目录不可用（%s）: %w", opts.StateDir, mkerr)
	}

	// ── 2. 日志：尽力打开，失败降级为 nil（静默丢弃，不阻断同步）。──
	var lg *logging.Logger
	if opts.LogPath != "" {
		if l, oerr := logging.Open(opts.LogPath); oerr == nil {
			lg = l
		}
	}
	defer func() {
		if lg != nil {
			_ = lg.Close()
		}
	}()

	// ── 2b. 单实例护栏：state/sync.lock（O_EXCL）。拿不到锁等 lockWait 后
	// 记日志并 return（nil, nil）→ 调用方 exit 0；陈旧锁（进程必然已死）
	// 删除重试自愈。──
	release, contended, lerr := acquireSyncLock(filepath.Join(opts.StateDir, "sync.lock"), lockWait, lockStaleAfter)
	if lerr != nil {
		return nil, lerr
	}
	defer release()
	if contended {
		lg.Info("", "sync.lock 被占用：另一实例运行中，等待"+lockWait.String()+"后放弃（exit 0）")
		return nil, nil
	}

	// ── 2c. 崩溃残留自愈（DSD §5.1）：清理 state 目录 mtime>1h 的 *.tmp。──
	if removed, cerr := state.CleanupStaleTmp(opts.StateDir, time.Hour); cerr != nil {
		lg.Warn("", "清理崩溃残留 tmp 失败: "+cerr.Error())
	} else if removed > 0 {
		lg.Info("", fmt.Sprintf("清理崩溃残留 tmp %d 个", removed))
	}

	// ── 3. 读 config：失败 → DefaultGlobalConfig 兜底 + last_error；成功 → Normalize。──
	var lastErrMsg string
	cfg := config.Default()
	if c, lerr := config.Load(opts.ConfigPath); lerr != nil {
		lastErrMsg = fmt.Sprintf("config 解析失败: %v", lerr)
		mg := "config 读取失败，回落默认配置: " + lastErrMsg
		lg.Warn("", mg)
	} else {
		cfg = c
		cfg.Normalize()
	}
	interval := time.Duration(cfg.IntervalMinutes) * time.Minute

	// ── 4. 消费 trigger：新鲜 sync_now → Force；reconfigure 仅记日志；过期忽略。──
	force := opts.Force
	if tr, consumed, terr := state.ConsumeTrigger(triggerPath); terr != nil {
		lg.Warn("", "trigger 消费失败: "+terr.Error())
	} else if consumed {
		switch {
		case !state.IsFresh(tr, time.Now(), triggerMaxAge):
			lg.Info("", "trigger 过期已忽略（request="+tr.RequestID+"）")
		case tr.Action == model.ActionSyncNow:
			force = true
			lg.Info("", "trigger sync_now 命中（request="+tr.RequestID+"）")
		case tr.Action == model.ActionReconfigure:
			lg.Info("", "trigger reconfigure（v1 无消费方，仅记日志）")
		}
	}

	// ── 5. 间隔门：未到点 → 写最小 status（waiting）并返回（exit 0）。──
	// 时间回拨：now.Before(lastRunAt+interval) 对负 elapsed 天然视同未到点。
	prev, _, _ := state.ReadStatus(statusPath) // 读失败视同无历史 → 本轮完整同步自愈
	if !force && prev != nil && now.Before(prev.Task.LastRunAt.Add(interval)) {
		st := minimalWaitingStatus(prev, now, interval, cfg.IntervalMinutes)
		if werr := state.WriteStatus(statusPath, st); werr != nil {
			return nil, fmt.Errorf("写 status.json 失败: %w", werr)
		}
		lg.Info("", "interval gate：未到同步间隔，等待窗口")
		return st, nil
	}

	// ── 5b. 全局停用开关（DSD §1.1）：GUI"暂停自动同步"写 enabled=false，
	// 任务端读 false 即跳过分发——写最小 status（刷新 updated_at 保证托盘
	// 新鲜度）后返回，不触碰解析与 hosts。force/trigger 一律不越过该开关。──
	if !cfg.Enabled {
		st := minimalWaitingStatus(prev, now, interval, cfg.IntervalMinutes)
		if werr := state.WriteStatus(statusPath, st); werr != nil {
			return nil, fmt.Errorf("写 status.json 失败: %w", werr)
		}
		lg.Info("", "config.enabled=false：全局停用，本轮跳过分发")
		return st, nil
	}

	// ── 6. 完整同步。──
	hostsContent, rerr := hostsfile.Read(opts.HostsPath)
	if rerr != nil {
		if !os.IsNotExist(rerr) {
			lg.Warn("", "读取 hosts 失败（按空内容处理）: "+rerr.Error())
		}
		hostsContent = nil
	}
	// 当前块既有行（失败/停用/无效条目的旧值保留依据，failure_keep_old）。
	curByTarget := linesByTarget(hostsfile.ParseBlock(hostsContent))

	entries := cfg.Entries
	statuses := make([]model.EntryStatus, 0, len(entries))
	// resolvedByTarget：本轮解析成功且有 IP 的条目 → 新行（覆盖旧值）。
	resolvedByTarget := map[string][]hostsfile.Line{}

	// 规则 4：enabled 条目 target 冲突仅保留序首个（其余标 invalid_config）。
	firstIDOfTarget := map[string]string{}
	for i := range entries {
		e := &entries[i]
		if !e.Enabled {
			continue
		}
		t := strings.ToLower(strings.TrimSpace(e.Target))
		if _, seen := firstIDOfTarget[t]; !seen {
			firstIDOfTarget[t] = e.ID
		}
	}

	// 6a. 遍历 entries（顺序执行，status 顺序与 config 一致）。
	for i := range entries {
		e := &entries[i]
		es := model.EntryStatus{ID: e.ID, Enabled: e.Enabled, ResolvedAt: now}

		if !e.Enabled {
			es.Status = model.EntryPaused // 不解析、不写、不删除 hosts 已有行
			statuses = append(statuses, es)
			continue
		}
		if verr := config.ValidateEntry(*e); verr != nil {
			es.Status = model.EntryInvalidConfig
			es.Error = verr.Error()
			lg.Warn(e.ID, "invalid_config: "+verr.Error())
			statuses = append(statuses, es)
			continue
		}
		t := strings.ToLower(strings.TrimSpace(e.Target))
		if firstID, ok := firstIDOfTarget[t]; ok && firstID != e.ID {
			es.Status = model.EntryInvalidConfig
			es.Error = fmt.Sprintf("target 重复，与条目 %s 冲突", firstID)
			lg.Warn(e.ID, "invalid_config: "+es.Error)
			statuses = append(statuses, es)
			continue
		}

		dnsCfg := cfg.DNS
		dnsCfg.MaxCNAMEDepth = cfg.MaxCNAMEDepth // 拷贝进 Resolve 签名所需的 DNSConfig
		res, rerr := resolveFn(opts, e.Source, dnsCfg)
		if rerr != nil {
			st := statusForError(rerr)
			es.Status = st
			es.Error = entryErrorMessage(st, rerr, cfg, e.Source)
			lg.Warn(e.ID, fmt.Sprintf("%s: %s", st, es.Error))
			statuses = append(statuses, es) // 旧值保留由装配步骤（curByTarget 继承）完成
			continue
		}
		es.Status = model.EntryOK
		es.ResolvedIPs = res.IPs
		es.ResolvedCNAMEChain = res.Chain
		es.ResolvedAt = res.ResolvedAt.UTC()
		es.UsedFallback = res.UsedFallback
		if res.UsedFallback {
			es.Error = "DoH 不可用已回退系统解析"
		}
		if len(res.IPs) > 0 {
			lines := make([]hostsfile.Line, 0, len(res.IPs))
			for _, ip := range res.IPs {
				lines = append(lines, hostsfile.Line{IP: ip, Target: e.Target})
			}
			resolvedByTarget[e.Target] = lines
		}
		lg.Info(e.ID, fmt.Sprintf("resolved %s chain=%q used_fallback=%v",
			strings.Join(res.IPs, ", "), strings.Join(res.Chain, "->"), res.UsedFallback))
		statuses = append(statuses, es)
	}

	// 6b. 装配期望块：OK 条目新行覆盖；未被覆盖的既有行仅当 target 仍存在于
	// config entries 中才继承（失败/停用/无效条目旧值保留，DSD §3.1/§3.3）；
	// 已从 config 删除/改 target 的旧行不再进入期望块（随替换自然清走，§4.2）。
	configTargets := make(map[string]bool, len(entries))
	for i := range entries {
		configTargets[strings.ToLower(strings.TrimSpace(entries[i].Target))] = true
	}
	merged := map[string][]hostsfile.Line{}
	for t, lns := range curByTarget {
		if configTargets[t] {
			merged[t] = lns
		}
	}
	for t, lns := range resolvedByTarget {
		if len(lns) > 0 {
			merged[t] = lns
		}
	}
	// 确定性装配：按 config 条目顺序输出行，保证同 IP 同族的稳定排序
	// （BuildBlock 内 sort.SliceStable 保持输入序，避免 map 顺序导致
	// 内容相同的块因行序翻转而出现"伪篡改"写盘）。块内未被任何条目
	// 声明的陌生 target 按字典序排尾。
	blockLines := make([]hostsfile.Line, 0, len(merged))
	placedTarget := make(map[string]bool, len(merged))
	place := func(t string) {
		if placedTarget[t] {
			return
		}
		placedTarget[t] = true
		blockLines = append(blockLines, merged[t]...)
	}
	for i := range entries {
		place(entries[i].Target)
	}
	var leftovers []string
	for t := range merged {
		if !placedTarget[t] {
			leftovers = append(leftovers, t)
		}
	}
	sort.Strings(leftovers)
	for _, t := range leftovers {
		place(t)
	}
	order, oerr := model.ParseIPOrder(cfg.DNS.IPVersion) // Normalize 已保证合法；双返回值防御
	if oerr != nil {
		order = model.OrderBoth
	}
	block, berr := hostsfile.BuildBlock(blockLines, order)
	if berr != nil {
		return nil, fmt.Errorf("装配期望块失败: %w", berr)
	}

	// 6c. 变化检测：content_md5（当前块）vs expected_md5（期望块全文 md5）。
	contentEOL := hostsfile.DetectEOL(hostsContent)
	expectedRegion := hostsfile.BlockText(block, contentEOL)
	expectedMD5 := md5Hex(expectedRegion)
	blockPresent := false
	curMD5 := ""
	if _, _, found := hostsfile.LocateBlock(hostsContent); found {
		blockPresent = true
		curMD5, _ = hostsfile.MD5Block(hostsContent)
	}
	// 6d. 相等且块存在 → 不写盘。
	changed := !blockPresent || curMD5 != expectedMD5
	lastWriteOK := true
	lastWriteAt := time.Time{}
	// 块缺失/篡改自愈标注（DSD §3.2）：写盘成功后才记录 warn 与 last_error。
	tamperDetected := changed && blockPresent && curMD5 != expectedMD5
	if changed {
		// 6e. 备份（失败仅 warn）→ ComposeFull → WriteAtomic（重试 3 次 500ms）。
		if _, berr := hostsfile.Backup(opts.HostsPath); berr != nil {
			msg := "warn: backup 失败: " + berr.Error()
			lg.Warn("", msg)
			lastErrMsg = joinMsg(lastErrMsg, msg)
		} else {
			lg.Info("", "backup 完成：hosts.bak-ddns-hosts-sync")
		}
		full, _, cerr := hostsfile.ComposeFull(hostsContent, block)
		if cerr != nil {
			return nil, fmt.Errorf("组装 hosts 全文失败: %w", cerr)
		}
		if werr := writeWithRetry(opts.HostsPath, full, contentEOL); werr != nil {
			lastWriteOK = false
			msg := "hosts 写盘失败，旧 hosts 保留原样: " + werr.Error()
			lg.Error("", "hosts write failed after 3 retries: "+werr.Error())
			lastErrMsg = joinMsg(lastErrMsg, msg)
			for j := range statuses {
				if statuses[j].Status == model.EntryOK {
					statuses[j].Status = model.EntryWriteFailed
					statuses[j].Error = "hosts 写盘失败，旧值保留（重试 3 次后放弃）"
				}
			}
		} else {
			lastWriteAt = time.Now().UTC()
			blockPresent = true
			curMD5 = expectedMD5
			if tamperDetected {
				msg := "检测到块缺失/篡改，已自动重写"
				lg.Warn("", msg)
				lastErrMsg = joinMsg(lastErrMsg, msg)
			}
			if cfg.FlushDNS && opts.FlushDNS != nil {
				if ferr := opts.FlushDNS(); ferr != nil {
					msg := "warn: flushdns 失败: " + ferr.Error()
					lg.Warn("", msg)
					lastErrMsg = joinMsg(lastErrMsg, msg)
				}
			}
		}
	}

	// 6f. HostsPresent 依据实际落盘状态（写失败时沿用写前 hosts）。
	actual := curByTarget
	if lastWriteOK {
		actual = merged
	}
	for j := range entries {
		statuses[j].HostsPresent = presentInTarget(actual, entries[j].Target)
	}

	// ── 7. 写 status.json + 日志摘要。──
	failedEntries := 0
	for _, es := range statuses {
		if es.Status != model.EntryOK && es.Status != model.EntryPaused {
			failedEntries++
		}
	}
	st := &model.SyncStatus{
		Version:         model.StatusVersion,
		UpdatedAt:       now,
		NextScheduledAt: now.Add(interval),
		IntervalMinutes: cfg.IntervalMinutes,
		SyncWindow:      model.WindowSynced,
		Task: model.TaskStatus{
			LastRunAt: now,
			LastRunOK: lastWriteOK,
			LastError: lastErrMsg,
		},
		Entries:    statuses,
		HostsBlock: hostsBlockState(blockPresent, curMD5, expectedMD5, lastWriteAt, lastWriteOK),
	}
	if werr := state.WriteStatus(statusPath, st); werr != nil {
		return nil, fmt.Errorf("写 status.json 失败: %w", werr)
	}
	changedInt := 0
	if changed {
		changedInt = 1
	}
	lg.Info("", fmt.Sprintf("summary entries=%d changed=%d failed=%d",
		len(statuses), changedInt, failedEntries))
	return st, nil
}

// ---------------------------------------------------------------------------
// 助手
// ---------------------------------------------------------------------------

// minimalWaitingStatus 构造 waiting 最小 status：保留上轮任务/条目/块状态
// （浅拷贝），仅刷新 updated_at（保证托盘新鲜度）、next_scheduled_at
// （now+interval 重算）、sync_window 与 interval_minutes。无历史时构造全新
// 空 status（仅版本字段）。
func minimalWaitingStatus(prev *model.SyncStatus, now time.Time, interval time.Duration, intervalMinutes int) *model.SyncStatus {
	var st model.SyncStatus
	if prev != nil {
		st = *prev
	} else {
		st = model.SyncStatus{Version: model.StatusVersion}
	}
	st.UpdatedAt = now
	st.NextScheduledAt = now.Add(interval)
	st.SyncWindow = model.WindowWaiting
	st.IntervalMinutes = intervalMinutes
	return &st
}

// resolveFn 解析入口：优先测试钩子，否则 resolver.Resolve。
func resolveFn(opts Options, source string, dnsCfg model.DNSConfig) (*resolver.ResolveResult, error) {
	if opts.Resolve != nil {
		return opts.Resolve(source, dnsCfg)
	}
	return resolver.Resolve(source, dnsCfg)
}

// statusForError 将 sentinel 错误映射为条目状态（DSD §3.1）；未匹配视为 timeout。
func statusForError(err error) model.EntryState {
	switch {
	case errors.Is(err, resolver.ErrNXDomain):
		return model.EntryNXDomain
	case errors.Is(err, resolver.ErrCNAMELoop):
		return model.EntryCNAMELoop
	case errors.Is(err, resolver.ErrTooDeep):
		return model.EntryTooDeep
	default:
		return model.EntryTimeout
	}
}

// entryErrorMessage DSD §3.1 各状态错误文案。
func entryErrorMessage(st model.EntryState, err error, cfg *config.Config, source string) string {
	switch st {
	case model.EntryNXDomain:
		return fmt.Sprintf("解析链上不存在域名 %s", source)
	case model.EntryCNAMELoop:
		return "检测到 CNAME 环: " + err.Error()
	case model.EntryTooDeep:
		return fmt.Sprintf("CNAME 链超过 %d 层", cfg.MaxCNAMEDepth)
	default:
		return fmt.Sprintf("解析超时（%ds），保留上一次条目", cfg.DNS.TimeoutSec)
	}
}

// linesByTarget 将块内行按 target 聚合。
func linesByTarget(lines []hostsfile.Line) map[string][]hostsfile.Line {
	out := make(map[string][]hostsfile.Line, len(lines))
	for _, ln := range lines {
		out[ln.Target] = append(out[ln.Target], ln)
	}
	return out
}

func presentInTarget(m map[string][]hostsfile.Line, target string) bool {
	_, ok := m[target]
	return ok
}

// writeWithRetry 写盘重试 3 次、500ms backoff（DSD §7.6：Windows rename 可能
// 瞬时 EACCES/EPERM）。最终失败返回最后一个错误。
func writeWithRetry(path string, full []byte, eol string) error {
	var lastErr error
	for attempt := 0; attempt < 3; attempt++ {
		if err := hostsfile.WriteAtomic(path, full, eol); err == nil {
			return nil
		} else {
			lastErr = err
		}
		if attempt < 2 {
			time.Sleep(500 * time.Millisecond)
		}
	}
	return lastErr
}

// hostsBlockState 组装 hosts_block 状态字段（DSD §1.6）。
func hostsBlockState(present bool, contentMD5, expectedMD5 string, lastWriteAt time.Time, lastWriteOK bool) model.HostsBlockStatus {
	return model.HostsBlockStatus{
		Present:     present,
		ContentMD5:  contentMD5,
		ExpectedMD5: expectedMD5,
		LastWriteAt: lastWriteAt,
		LastWriteOK: lastWriteOK,
	}
}

func joinMsg(a, b string) string {
	if a == "" {
		return b
	}
	if b == "" {
		return a
	}
	return a + "; " + b
}

func md5Hex(b []byte) string {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:])
}

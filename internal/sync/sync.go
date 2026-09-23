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
	"os/exec"
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
}

// triggerMaxAge 触发新鲜度阈值（DSD §1.3：≤2 分钟视为有效）。
const triggerMaxAge = 2 * time.Minute

// Run 执行一次完整循环（sync 子命令入口）。返回最终落盘 status 快照；
// 非 nil error 表示程序自身致命错误（状态目录不可用等），调用方应退出非零。
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
		st := *prev // 浅拷贝：保留上次任务/条目/块状态，仅刷新窗口字段
		st.UpdatedAt = now
		st.NextScheduledAt = now.Add(interval)
		st.SyncWindow = model.WindowWaiting
		st.IntervalMinutes = cfg.IntervalMinutes
		if werr := state.WriteStatus(statusPath, &st); werr != nil {
			return nil, fmt.Errorf("写 status.json 失败: %w", werr)
		}
		lg.Info("", "interval gate：未到同步间隔，等待窗口")
		return &st, nil
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

	// 6b. 装配期望块：OK 条目新行覆盖 + 未被覆盖的既有行原样继承
	// （失败/停用/无效条目旧值保留，DSD §3.1/§3.3 failure_keep_old）。
	merged := map[string][]hostsfile.Line{}
	for t, lns := range curByTarget {
		merged[t] = lns
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
	if changed && blockPresent && curMD5 != expectedMD5 {
		// 块缺失/篡改自愈（DSD §3.2）。
		msg := "检测到块缺失/篡改，已自动重写"
		lg.Warn("", msg)
		lastErrMsg = joinMsg(lastErrMsg, msg)
	}
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
			if cfg.FlushDNS {
				if ferr := flushDNSCache(); ferr != nil {
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

// flushDNSCache Linux best-effort DNS 缓存刷新：尝试 resolvectl flush-caches
// 或 systemd-resolve --flush-caches，全部失败才报错（调用方仅 warn）。
// TODO M2: 移入 platform 包按 OS 分发（Windows ipconfig /flushdns、
// macOS dscacheutil -flushcache）。
func flushDNSCache() error {
	cmds := [][]string{
		{"resolvectl", "flush-caches"},
		{"systemd-resolve", "--flush-caches"},
	}
	for _, c := range cmds {
		if _, err := exec.LookPath(c[0]); err != nil {
			continue
		}
		if err := exec.Command(c[0], c[1:]...).Run(); err == nil {
			return nil
		}
	}
	return errors.New("resolvectl/systemd-resolve 均不可用或执行失败")
}

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

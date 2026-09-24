package gui

import (
	"os"
	"strings"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/widget"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
	"github.com/heliopause/ddns-hosts-sync/internal/state"
)

// ---------------------------------------------------------------------------
// 状态文件轮询（DSD §4.1/§4.5）：mtime 变化才重解析，rename 瞬时失败重试一次
// ---------------------------------------------------------------------------

// statusWatcher status.json 增量轮询器（单 goroutine 使用；可测）。
type statusWatcher struct {
	path string

	haveFile  bool
	lastMtime time.Time
	lastSize  int64
	cached    *model.SyncStatus
}

func newStatusWatcher(path string) *statusWatcher {
	return &statusWatcher{path: path}
}

// poll 一次轮询：
//   - 文件不存在 → (nil, false, nil)（未变化）；
//   - mtime+size 与上次相同 → 返回缓存（changed=false）；
//   - 变化 → 读取解析；瞬时 rename 失败（JSON 解析错/读错）→ 50ms 后重试一次；
//   - 首次读到 → changed=true。
func (w *statusWatcher) poll() (*model.SyncStatus, bool, error) {
	info, err := os.Stat(w.path)
	if err != nil {
		if os.IsNotExist(err) {
			if w.haveFile {
				w.haveFile = false
				w.lastMtime = time.Time{}
				w.cached = nil
				return nil, true, nil // 状态文件被移除（如卸载）
			}
			return nil, false, nil
		}
		return nil, false, err
	}
	if w.haveFile && info.ModTime().Equal(w.lastMtime) && info.Size() == w.lastSize {
		return w.cached, false, nil
	}

	st, ok, err := state.ReadStatus(w.path)
	if err != nil || !ok {
		// rename 窗口内的瞬时失败：重试一次（50ms）后放弃。
		time.Sleep(50 * time.Millisecond)
		st, ok, err = state.ReadStatus(w.path)
		if err != nil || !ok {
			return nil, false, err
		}
	}
	w.haveFile = true
	w.lastMtime = info.ModTime()
	w.lastSize = info.Size()
	w.cached = st
	return st, true, nil
}

// indexStatus 将 entries 列表转为 id → 状态 的查询表。
func indexStatus(st *model.SyncStatus) map[string]model.EntryStatus {
	m := make(map[string]model.EntryStatus, len(st.Entries))
	for i := range st.Entries {
		m[st.Entries[i].ID] = st.Entries[i]
	}
	return m
}

// ---------------------------------------------------------------------------
// 状态面板（DSD §4.4 字段映射）
// ---------------------------------------------------------------------------

// 面板字段键（stLabels 表）。
const (
	stSyncTime   = "sync_time"
	stNextTime   = "next_time"
	stWindow     = "window"
	stTaskHealth = "task_health"
	stHostsBlock = "hosts_block"
	stEntriesSum = "entries_summary"
)

// buildStatusPanel 状态 Tab：只读 Labels + 日志视图（DSD §4.1）。
func (g *GUI) buildStatusPanel() fyne.CanvasObject {
	panel := container.NewVBox(
		g.statusField(stSyncTime, "最近同步: --"),
		g.statusField(stNextTime, "下次同步: --"),
		g.statusField(stWindow, "本轮窗口: --"),
		g.statusField(stTaskHealth, "任务健康: --"),
		wrapLabel(g.statusField(stHostsBlock, "hosts 块: --")),
		g.statusField(stEntriesSum, "条目状态: --"),
	)

	logHeader := container.NewHBox(
		widget.NewButtonWithIcon("打开日志目录", fyne.NewStaticResource("folder", []byte{}), func() {
			if g.deps.OpenLogsDir != nil {
				g.deps.OpenLogsDir()
			}
		}),
		widget.NewButton("刷新日志", func() { g.refreshLogs(true) }),
		widget.NewLabel("最近 200 行"),
	)
	g.logList = widget.NewList(
		func() int {
			g.mu.Lock()
			defer g.mu.Unlock()
			return len(g.logLines)
		},
		func() fyne.CanvasObject { return widget.NewLabel("") },
		func(id widget.ListItemID, obj fyne.CanvasObject) {
			g.mu.Lock()
			line := ""
			if int(id) < len(g.logLines) {
				line = g.logLines[id]
			}
			g.mu.Unlock()
			obj.(*widget.Label).SetText(line)
		},
	)
	logScroll := container.NewVScroll(g.logList)
	logScroll.SetMinSize(fyne.NewSize(0, 260))

	return container.NewBorder(panel, logHeader, nil, nil, logScroll)
}

// statusField 创建带键值的字段包装（含展开细节）。
func (g *GUI) statusField(key, label string) *widget.Label {
	l := widget.NewLabel(label)
	l.Wrapping = fyne.TextWrapWord
	g.stLabels[key] = l
	return l
}

// wrapLabel Label 换行并限宽粘贴到面板（fyne VBox 全宽）。
func wrapLabel(l *widget.Label) *widget.Label { return l }

// refreshStatusPanel 依据内存 status 刷新全部面板字段（fyne 主循环内调用）。
func (g *GUI) refreshStatusPanel() {
	g.mu.Lock()
	st := g.st
	statusMap := g.statusMap
	g.mu.Unlock()

	if st == nil {
		for _, l := range g.stLabels {
			if l != nil {
				l.SetText("--")
			}
		}
	} else {
		setText := func(key, text string) {
			if g.stLabels[key] != nil {
				g.stLabels[key].SetText(text)
			}
		}
		setText(stSyncTime, "最近同步: "+timeFmt(st.UpdatedAt))
		setText(stNextTime, "下次同步: "+timeFmt(st.NextScheduledAt))
		setText(stWindow, "本轮窗口: "+windowText(st.SyncWindow))
		setText(stTaskHealth, "任务健康: "+taskHealthText(&st.Task))
		setText(stHostsBlock, "hosts 块: "+hostsBlockText(&st.HostsBlock))
		setText(stEntriesSum, "条目状态: "+g.entriesSummaryText(st, statusMap))
	}

	// 状态栏最近/下次摘要。
	g.updateStatusBarSummary(st)

	// 日志尾部（带 mtime 缓存）。
	g.refreshLogs(false)
}

// updateStatusBarSummary 更新窗口底部"最近同步/下次"摘要。
func (g *GUI) updateStatusBarSummary(st *model.SyncStatus) {
	// 底部状态栏字段暂由面板承担；预留（避免不必要引用）。
	_ = st
}

// timeFmt 本地时区展示（nil/零值 → "--"）。
func timeFmt(t time.Time) string {
	if t.IsZero() {
		return "--"
	}
	return t.Local().Format("2006-01-02 15:04:05")
}

// taskHealthText 任务健康（§4.4）。
func taskHealthText(t *model.TaskStatus) string {
	if t == nil {
		return "--"
	}
	s := "运行正常"
	if !t.LastRunOK {
		s = "异常"
	}
	if t.LastError != "" {
		s += "（" + strings.TrimSpace(t.LastError) + "）"
	}
	return s
}

// hostsBlockText hosts 块状态（含哈希失配红色语义提示，§3.2/§4.4）。
func hostsBlockText(h *model.HostsBlockStatus) string {
	if h == nil {
		return "--"
	}
	var parts []string
	if h.Present {
		parts = append(parts, "存在")
	} else {
		parts = append(parts, "缺失")
	}
	if h.ContentMD5 != "" && h.ExpectedMD5 != "" && h.ContentMD5 != h.ExpectedMD5 {
		parts = append(parts, "哈希失配（内容被外部修改）")
	}
	if h.LastWriteOK {
		parts = append(parts, "上次写入成功")
	} else {
		parts = append(parts, "⛔ 上次写入失败")
	}
	if !h.LastWriteAt.IsZero() {
		parts = append(parts, "写入于 "+h.LastWriteAt.Local().Format("15:04:05"))
	}
	return strings.Join(parts, " ｜ ")
}

// windowText 本轮窗口展示（DSD §1.4）。
func windowText(w model.SyncWindowState) string {
	if w == model.WindowSynced {
		return "已同步"
	}
	return "等待中（未到间隔）"
}

// entriesSummaryText 条目状态摘要（§4.4：徽标 + CNAME 链 + 回退标记 + IP）。
func (g *GUI) entriesSummaryText(st *model.SyncStatus, m map[string]model.EntryStatus) string {
	g.mu.Lock()
	var entries []model.Entry
	if g.cfg != nil {
		entries = append(entries, g.cfg.Entries...)
	}
	g.mu.Unlock()
	if len(entries) == 0 {
		return "(无条目)"
	}
	lines := make([]string, 0, len(entries))
	for _, e := range entries {
		s := m[e.ID]
		bits := []string{e.ID, " " + string(s.Status)}
		if s.UsedFallback {
			bits = append(bits, "（回退解析）")
		}
		if len(s.ResolvedCNAMEChain) > 0 {
			bits = append(bits, " 链:"+strings.Join(s.ResolvedCNAMEChain, " -> "))
		}
		if len(s.ResolvedIPs) > 0 {
			bits = append(bits, " IP:"+strings.Join(s.ResolvedIPs, ", "))
		}
		if s.Error != "" {
			bits = append(bits, " 错误:"+strings.TrimSpace(s.Error))
		}
		lines = append(lines, strings.Join(bits, ""))
	}
	return strings.Join(lines, "\n")
}

// ---------------------------------------------------------------------------
// 日志视图（最近 200 行滚动 + 打开日志目录，DSD §3.2/§4.1）
// ---------------------------------------------------------------------------

const logTailLines = 200

// refreshLogs 读取日志尾部（mtime/size 变化才重读）。force 强制重读。
func (g *GUI) refreshLogs(force bool) {
	info, err := os.Stat(g.deps.LogPath)
	if err != nil {
		return
	}
	if !force && g.logMtime.Equal(info.ModTime()) && g.logSize == info.Size() {
		return
	}
	lines, ferr := readLogTail(g.deps.LogPath, logTailLines)
	if ferr != nil {
		return
	}
	g.mu.Lock()
	g.logLines = lines
	g.mu.Unlock()
	g.logMtime = info.ModTime()
	g.logSize = info.Size()
	if g.logList != nil {
		g.logList.Refresh()
	}
}

// readLogTail 读取文件末尾 maxLines 行；文件不存在返回空（nil error）。
// 行按原顺序保留（最后一行可能为空行，展示时忽略）。
func readLogTail(path string, maxLines int) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return []string{}, nil
		}
		return nil, err
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	// 末尾空行剔除。
	for len(lines) > 0 && lines[len(lines)-1] == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return lines, nil
}

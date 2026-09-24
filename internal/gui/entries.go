package gui

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/data/binding"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// ---------------------------------------------------------------------------
// 条目表格（DSD §4.1：6 列）+ CRUD（§4.2 交互表）。
//
// 性能策略（DSD §7.8）：Table 的 CreateCell/UpdateCell 对象池复用；10s 状态
// 轮询仅对状态列调用 RefreshItem 单格刷新，不整表 Refresh。
// ---------------------------------------------------------------------------

// entryTable 包装 widget.Table：叠加 fyne.DoubleTappable（fyne Table 本体仅
// 实现 Tappable）。双击 → 编辑当前选中行（fyne 双击前必先触发单击选中，
// 故 activeRow 已指向命中行，DSD §4.2"双击行进入编辑"）。
type entryTable struct {
	*widget.Table
	onDoubleTap func()
}

func (t *entryTable) DoubleTapped(_ *fyne.PointEvent) {
	if t.onDoubleTap != nil {
		t.onDoubleTap()
	}
}

// buildTable 组装条目表格。
func (g *GUI) buildTable() fyne.CanvasObject {
	t := widget.NewTable(
		func() (int, int) { return g.tableLength() },
		func() fyne.CanvasObject { return g.createCellTemplate() },
		func(id widget.TableCellID, obj fyne.CanvasObject) { g.updateCell(id, obj) },
	)
	t.ShowHeaderRow = true
	t.CreateHeader = func() fyne.CanvasObject { return widget.NewLabel("") }
	t.UpdateHeader = func(id widget.TableCellID, obj fyne.CanvasObject) {
		if id.Col < 0 || id.Col >= numCols {
			return
		}
		obj.(*widget.Label).SetText(g.headerTitle(id.Col))
	}
	t.SetColumnWidth(colEnable, 56)
	t.SetColumnWidth(colSource, 200)
	t.SetColumnWidth(colTarget, 200)
	t.SetColumnWidth(colStatus, 180)
	t.SetColumnWidth(colIPs, 180)
	t.SetColumnWidth(colNote, 170)
	// M3：追踪表格选中行（TrackedSelection），工具栏编辑/删除/上移/下移/启停
	// 均以 activeRow 为锚点（DSD §4.2）。
	t.OnSelected = func(id widget.TableCellID) {
		g.setActiveRow(id.Row)
	}
	g.table = t
	w := &entryTable{Table: t, onDoubleTap: func() { g.onEditEntry() }}
	g.tableWrap = w
	return w
}

// tableLength 表格数据行数 = entries 条数。fyne Table 的表头行由
// ShowHeaderRow 叠加绘制（Length 只计数据行，UpdateHeader 以 Row=-1 标记
// 表头），不再把表头计入行数——此前 +1 导致多渲染一行空数据。
func (g *GUI) tableLength() (int, int) {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	if g.cfg != nil {
		n = len(g.cfg.Entries)
	}
	return n, numCols
}

// headerTitle 列标题（DSD §4.1）。
func (g *GUI) headerTitle(col int) string {
	switch col {
	case colEnable:
		return "启用"
	case colSource:
		return "源域名"
	case colTarget:
		return "目标域名"
	case colStatus:
		return "状态"
	case colIPs:
		return "最近 IP"
	default:
		return "备注"
	}
}

// createCellTemplate 创建单元格模板（每格复合对象：Check + Label，
// UpdateCell 按列切换可见性，符合 Table 对象池复用机制）。
func (g *GUI) createCellTemplate() fyne.CanvasObject {
	check := widget.NewCheck("", nil)
	label := widget.NewLabel("")
	label.Truncation = fyne.TextTruncateEllipsis
	label.Wrapping = fyne.TextTruncate
	return container.NewMax(check, label)
}

// cellWidgets 从复合模板拆出 Check/Label。
func cellWidgets(obj fyne.CanvasObject) (*widget.Check, *widget.Label) {
	w := obj.(*fyne.Container)
	var check *widget.Check
	var label *widget.Label
	for _, o := range w.Objects {
		switch v := o.(type) {
		case *widget.Check:
			check = v
		case *widget.Label:
			label = v
		}
	}
	return check, label
}

// updateCell 绑定单元格内容（行 0 = 表头由 CreateHeader 处理；数据行从 1 起，
// 列 0 = Check，其余 Label；同一 cell 对象跨轮询复用）。
func (g *GUI) updateCell(id widget.TableCellID, obj fyne.CanvasObject) {
	check, label := cellWidgets(obj)
	if check == nil || label == nil {
		return
	}
	if id.Col == colEnable {
		label.Hide()
		entry, ok := g.entryAt(id.Row)
		check.SetChecked(ok && entry.Enabled)
		check.OnChanged = func(on bool) {
			if e, ok := g.entryAt(id.Row); ok && e.Enabled != on {
				g.setEntryEnabledAt(id.Row, on)
			}
		}
		check.Show()
		return
	}

	check.Hide()
	label.SetText(g.cellText(id.Row, id.Col))
	label.Show()
}

// cellText 单元格文本（§4.4 字段映射）。
func (g *GUI) cellText(row, col int) string {
	entry, ok := g.entryAt(row)
	if !ok {
		return ""
	}
	g.mu.Lock()
	st := g.statusMap[entry.ID]
	g.mu.Unlock()
	switch col {
	case colSource:
		return entry.Source
	case colTarget:
		return entry.Target
	case colStatus:
		return statusBadge(st)
	case colIPs:
		return strings.Join(st.ResolvedIPs, "\n")
	case colNote:
		return entry.Note
	}
	return ""
}

// statusBadge 状态徽标文本（DSD §1.5 枚举 → 文案；§4.4 回退标记/错误摘要）。
func statusBadge(st model.EntryStatus) string {
	if st.ID == "" && st.Status == "" && st.Error == "" {
		return "—"
	}
	var b string
	switch st.Status {
	case model.EntryOK:
		b = "OK"
	case model.EntryNXDomain:
		b = "NXDOMAIN"
	case model.EntryCNAMELoop:
		b = "CNAME环"
	case model.EntryTooDeep:
		b = "超深度"
	case model.EntryTimeout:
		b = "超时"
	case model.EntryWriteFailed:
		b = "写盘失败"
	case model.EntryPaused:
		b = "已暂停"
	case model.EntryInvalidConfig:
		b = "配置非法"
	case "":
		b = "—"
	default:
		b = string(st.Status)
	}
	if st.UsedFallback {
		b += "（回退）"
	}
	if st.Error != "" {
		b += "：" + strings.TrimSpace(st.Error)
	}
	return b
}

// entryAt 取第 row 行（0 基数据行）的条目。
func (g *GUI) entryAt(row int) (model.Entry, bool) {
	g.mu.Lock()
	defer g.mu.Unlock()
	if g.cfg == nil || row < 0 || row >= len(g.cfg.Entries) {
		return model.Entry{}, false
	}
	return g.cfg.Entries[row], true
}

// setEntryEnabledAt 切换条目标记并即时写 config（§4.2 自动保存语义）。
func (g *GUI) setEntryEnabledAt(row int, on bool) {
	g.mu.Lock()
	if g.cfg == nil || row < 0 || row >= len(g.cfg.Entries) {
		g.mu.Unlock()
		return
	}
	g.cfg.Entries[row].Enabled = on
	g.mu.Unlock()
	_ = g.saveConfig()
}

// refreshEntryCellsStatus 轮询后仅刷新可见状态列（性能策略，fyne 主循环调用）。
func (g *GUI) refreshEntryCellsStatus() {
	if g.table == nil {
		return
	}
	g.mu.Lock()
	n := 0
	if g.cfg != nil {
		n = len(g.cfg.Entries)
	}
	g.mu.Unlock()
	for row := 0; row < n; row++ {
		g.table.RefreshItem(widget.TableCellID{Row: row, Col: colStatus})
	}
}

// ---------------------------------------------------------------------------
// CRUD 交互（DSD §4.2）
// ---------------------------------------------------------------------------

// onAddEntry 新增条目。
func (g *GUI) onAddEntry() {
	g.showEntryDialog(model.Entry{Enabled: true, ID: newEntryID()})
}

// onEditEntry 编辑选中条目。
func (g *GUI) onEditEntry() {
	if entry, ok := g.entryAt(g.activeRow); ok {
		g.showEntryDialog(entry)
	}
}

// onDeleteEntry 删除选中条目（二次确认；删除后立即写 config，hosts 该行由
// 下轮同步清理，DSD §4.2）。
func (g *GUI) onDeleteEntry() {
	entry, ok := g.entryAt(g.activeRow)
	if !ok {
		return
	}
	dialog.ShowConfirm("删除条目",
		"确定删除条目 "+entry.Source+" → "+entry.Target+" 吗？\nhosts 中的对应行将由下次同步清理。",
		func(ok bool) {
			if !ok {
				return
			}
			g.deleteEntryAt(g.activeRow)
		}, g.win)
}

// deleteEntryAt 删除指定行并即时写 config（确认对话框回调内调用，抽离以便
// 单测）。删除后活动行复位（M3：防止 activeRow 指向已位移/越界的数据行）。
func (g *GUI) deleteEntryAt(row int) {
	g.mu.Lock()
	if g.cfg == nil || row < 0 || row >= len(g.cfg.Entries) {
		g.mu.Unlock()
		return
	}
	g.cfg.Entries = append(g.cfg.Entries[:row], g.cfg.Entries[row+1:]...)
	g.mu.Unlock()
	if err := g.saveConfig(); err != nil {
		return
	}
	g.setActiveRow(-1)
	g.tableRefresh()
}

// moveEntry 上移/下移交换（写入 config 顺序，§4.2 排序）。
func (g *GUI) moveEntry(delta int) {
	g.mu.Lock()
	if g.cfg == nil || len(g.cfg.Entries) == 0 {
		g.mu.Unlock()
		return
	}
	row, to := g.activeRow, g.activeRow+delta
	if row < 0 || to < 0 || to >= len(g.cfg.Entries) {
		g.mu.Unlock()
		return
	}
	g.cfg.Entries[row], g.cfg.Entries[to] = g.cfg.Entries[to], g.cfg.Entries[row]
	g.mu.Unlock()
	if err := g.saveConfig(); err != nil {
		return
	}
	g.setActiveRow(to)
	g.tableRefresh()
}

// onToggleEnabled 工具栏"启用/停用"：切换选中条目并即时写 config。
func (g *GUI) onToggleEnabled() {
	row := g.activeRow
	e, ok := g.entryAt(row)
	if !ok {
		return
	}
	g.setEntryEnabledAt(row, !e.Enabled)
	if g.table != nil {
		g.table.RefreshItem(widget.TableCellID{Row: row, Col: colEnable})
	}
}

// tableRefresh 安全整表刷新（主循环内）。
func (g *GUI) tableRefresh() {
	if g.table != nil {
		g.table.Refresh()
	}
}

// ---------------------------------------------------------------------------
// 新增/编辑对话框（失焦即时校验 + 提交权威校验；失败不关闭，DSD §4.2）
// ---------------------------------------------------------------------------

// entryDialog 编辑对话框组件（dialog.NewCustom 自管理"保存"按钮，校验失败
// 不 Hide——fyne dialog 无拦截回调，此为可控实现并作为已知取舍记录）。
type entryDialog struct {
	gui    *GUI
	isEdit bool
	id     string
	dlg    dialog.Dialog

	srcEntry, tgtEntry, noteEntry binding.String
	srcW, tgtW, noteW             *widget.Entry
	check                         *widget.Check
	errLabel                      *widget.Label
}

var errNoteTooLong = errors.New("备注过长（上限 200 字符）")

// showEntryDialog 打开新增/编辑对话框。
func (g *GUI) showEntryDialog(entry model.Entry) {
	isEdit := entry.ID != ""
	if entry.ID == "" {
		entry.ID = newEntryID()
	}

	d := &entryDialog{
		gui:       g,
		isEdit:    isEdit,
		id:        entry.ID,
		srcEntry:  binding.NewString(),
		tgtEntry:  binding.NewString(),
		noteEntry: binding.NewString(),
		errLabel:  widget.NewLabel(""),
	}
	d.errLabel.Wrapping = fyne.TextWrapWord
	_ = d.srcEntry.Set(entry.Source)
	_ = d.tgtEntry.Set(entry.Target)
	_ = d.noteEntry.Set(entry.Note)
	d.srcW = widget.NewEntryWithData(d.srcEntry)
	d.tgtW = widget.NewEntryWithData(d.tgtEntry)
	d.noteW = widget.NewEntryWithData(d.noteEntry)
	d.check = widget.NewCheck("启用", nil)
	d.check.SetChecked(entry.Enabled)
	d.wireValidators()

	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "源域名", Widget: d.srcW},
			{Text: "目标域名", Widget: d.tgtW},
			{Text: "备注", Widget: d.noteW},
			{Text: "", Widget: d.check},
		},
		SubmitText: "保存",
		OnSubmit:   d.commit,
	}

	dlgTitle := "新增条目"
	if isEdit {
		dlgTitle = "编辑条目"
	}
	d.dlg = dialog.NewCustom(dlgTitle, "取消",
		container.NewVBox(form, d.errLabel), g.win)
	d.dlg.Show()
}

// wireValidators 失焦即时校验（体验校验；空值放行，提交校验兜底规则 2/3）。
// fyne Entry 以 Validator 字段注入（无 SetValidator 方法，v2.8）。
func (d *entryDialog) wireValidators() {
	d.srcW.Validator = func(s string) error {
		if s == "" {
			return nil
		}
		e := model.Entry{Source: strings.TrimSpace(s), Target: "placeholder.example.com"}
		return config.ValidateEntry(e)
	}
	d.tgtW.Validator = func(s string) error {
		if s == "" {
			return nil
		}
		e := model.Entry{Source: "placeholder.example.com", Target: strings.TrimSpace(s)}
		return config.ValidateEntry(e)
	}
	d.noteW.Validator = func(s string) error {
		if len([]rune(s)) > noteMaxRunes {
			return errNoteTooLong
		}
		return nil
	}
}

// commit 提交权威校验（规则 2/3 + 全局 target 去重），失败显示错误不关闭。
func (d *entryDialog) commit() {
	src, _ := d.srcEntry.Get()
	tgt, _ := d.tgtEntry.Get()
	note, _ := d.noteEntry.Get()

	e := model.Entry{
		ID:      d.id,
		Source:  src,
		Target:  tgt,
		Enabled: d.check.Checked,
		Note:    note,
	}
	config.NormalizeEntry(&e)

	var errs []string
	if err := validateEntryInput(e); err != nil {
		errs = append(errs, err.Error())
	} else if err := d.validateGlobalUnique(e); err != nil {
		errs = append(errs, err.Error())
	}

	if len(errs) > 0 {
		d.errLabel.SetText("校验未通过：\n" + strings.Join(errs, "\n"))
		d.errLabel.Show()
		return // 校验失败不关闭对话框
	}

	d.gui.upsertEntry(e)
	d.dlg.Hide()
}

// validateGlobalUnique 规则 4：target 全部 enabled 条目全局唯一。
func (d *entryDialog) validateGlobalUnique(e model.Entry) error {
	d.gui.mu.Lock()
	all := append([]model.Entry(nil), d.gui.cfg.Entries...)
	d.gui.mu.Unlock()
	if d.isEdit {
		// 替换自身后校验（去重排除自身）。
		replaced := make([]model.Entry, 0, len(all))
		for _, x := range all {
			if x.ID == e.ID {
				replaced = append(replaced, e)
			} else {
				replaced = append(replaced, x)
			}
		}
		return validateEntryUnique(replaced, e.ID)
	}
	all = append(all, e)
	return validateEntryUnique(all, "")
}

// upsertEntry 写入内存配置并落盘（新增/编辑共用）。
func (g *GUI) upsertEntry(e model.Entry) {
	g.mu.Lock()
	if g.cfg == nil {
		g.cfg = config.Default()
	}
	found := false
	for i := range g.cfg.Entries {
		if g.cfg.Entries[i].ID == e.ID {
			g.cfg.Entries[i] = e
			found = true
			break
		}
	}
	if !found {
		g.cfg.Entries = append(g.cfg.Entries, e)
	}
	g.mu.Unlock()
	if err := g.saveConfig(); err != nil {
		return
	}
	g.tableRefresh()
}

// ---------------------------------------------------------------------------
// 辅助
// ---------------------------------------------------------------------------

// newEntryID 生成 8 字符十六进制短码（DSD §1.2：UUID 短码；crypto/rand 无
// 时序可预测性，长度对齐既有约定）。
func newEntryID() string {
	b := make([]byte, 4)
	if _, err := rand.Read(b); err != nil {
		return "e-00000000"
	}
	return "e-" + hex.EncodeToString(b)
}

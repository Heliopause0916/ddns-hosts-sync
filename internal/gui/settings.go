package gui

import (
	"fmt"
	"net/url"
	"strconv"
	"strings"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/container"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// 全局设置值域（DSD §1.1）。
const (
	intervalMin, intervalMax     = 1, 1440
	dohTimeoutMin, dohTimeoutMax = 5, 120
)

// buildSettings 全局设置 Tab：Form（DSD §4.1）。
// 布局：轮询间隔(Entry)｜DNS模式(Select)｜DoH服务器(Entry 多行)｜IP版本(Select)
// ｜DNS刷新(Check)｜暂停自动同步(Check) ｜ [保存] [恢复默认]。
func (g *GUI) buildSettings() fyne.CanvasObject {
	g.gInterval = widget.NewEntry()
	g.gInterval.SetPlaceHolder(fmt.Sprintf("%d", intervalMin))
	g.gInterval.Validator = func(s string) error { return validateInterval(s) }

	g.gDNSMode = widget.NewSelect([]string{"doh", "system"}, nil)

	g.gDOH = widget.NewEntry()
	g.gDOH.MultiLine = true
	g.gDOH.SetMinRowsVisible(3)
	g.gDOH.SetPlaceHolder("每行一个 DoH 服务器 URL")

	g.gIPVer = widget.NewSelect([]string{"ipv4", "ipv6", "both"}, nil)

	g.gFlush = widget.NewCheck("写盘后刷新系统 DNS 缓存", nil)
	// 暂停自动同步：即时写 config（自动保存语义，DSD §4.1；测试覆盖落盘）。
	g.gPause = widget.NewCheck("暂停自动同步（后台任务跳过同步）", func(on bool) {
		g.mu.Lock()
		if g.cfg != nil {
			g.cfg.Enabled = !on
		}
		g.mu.Unlock()
		_ = g.saveConfig()
	})

	g.loadSettingsForm()

	form := &widget.Form{
		Items: []*widget.FormItem{
			{Text: "轮询间隔（分钟）", Widget: g.gInterval},
			{Text: "DNS 模式", Widget: g.gDNSMode},
			{Text: "DoH 服务器", Widget: g.gDOH},
			{Text: "IP 版本", Widget: g.gIPVer},
			{Text: "", Widget: g.gFlush},
			{Text: "", Widget: g.gPause},
		},
		SubmitText: "保存",
		OnSubmit:   g.onSaveSettings,
		CancelText: "恢复默认",
		OnCancel:   g.onResetSettings,
	}
	return container.NewBorder(nil, nil, nil, nil, form)
}

// loadSettingsForm 从内存配置填充表单。
func (g *GUI) loadSettingsForm() {
	g.mu.Lock()
	cfg := g.cfg.GlobalConfig
	g.mu.Unlock()
	if g.gInterval != nil {
		g.gInterval.SetText(strconv.Itoa(cfg.IntervalMinutes))
	}
	if g.gDNSMode != nil {
		g.gDNSMode.SetSelected(cfg.DNS.Mode)
	}
	if g.gDOH != nil {
		g.gDOH.SetText(strings.Join(cfg.DNS.DOHServers, "\n"))
	}
	if g.gIPVer != nil {
		g.gIPVer.SetSelected(cfg.DNS.IPVersion)
	}
	if g.gFlush != nil {
		g.gFlush.SetChecked(cfg.FlushDNS)
	}
	if g.gPause != nil && g.cfg != nil {
		g.gPause.SetChecked(!g.cfg.Enabled)
	}
}

// onSaveSettings 批量保存全局设置（事务语义：校验全通过才落盘）。
func (g *GUI) onSaveSettings() {
	nc, errs := g.collectGlobalSettings()
	if len(errs) > 0 {
		g.showBanner(strings.Join(errs, "；"), bannerErr)
		return
	}
	g.mu.Lock()
	g.cfg.GlobalConfig = nc
	g.mu.Unlock()
	if err := g.saveConfig(); err != nil {
		return
	}
	g.showBanner("全局设置已保存，最迟 1 分钟生效", bannerOK)
	g.refreshStatusPanel()
}

// onResetSettings 恢复默认（确认后覆盖内存并落盘）。
func (g *GUI) onResetSettings() {
	dialog.ShowConfirm("恢复默认设置",
		"确定恢复全部默认设置吗？条目列表不受影响。",
		func(ok bool) {
			if !ok {
				return
			}
			def := config.Default()
			g.mu.Lock()
			saved := g.cfg
			g.cfg = def // 保留条目
			g.cfg.Entries = append([]model.Entry(nil), saved.Entries...)
			g.mu.Unlock()
			if err := g.saveConfig(); err != nil {
				return
			}
			g.loadSettingsForm()
			g.setPauseCheck(!g.cfg.Enabled)
			g.showBanner("已恢复默认设置", bannerOK)
		}, g.win)
}

// setPauseCheck 同步"暂停自动同步"勾选（恢复默认后），避免二次触发写盘。
func (g *GUI) setPauseCheck(on bool) {
	if g.gPause != nil && g.gPause.Checked != on {
		g.gPause.SetChecked(on)
	}
}

// collectGlobalSettings 收集并校验表单值（返回标准化结果或错误列表）。
// M6 修复：暂停勾选态随表单读取（此前硬编码 nc.Enabled=true，批量保存会把
// 暂停中的 enabled 重置为 true，违背 DSD §1.1"暂停自动同步"语义）。
func (g *GUI) collectGlobalSettings() (model.GlobalConfig, []string) {
	nc := model.DefaultGlobalConfig()
	nc.Enabled = !g.gPause.Checked // 暂停勾选即 enabled=false（读取表单，不覆盖暂停态）
	var errs []string

	iv, err := strconv.Atoi(strings.TrimSpace(g.gInterval.Text))
	if err != nil || iv < intervalMin || iv > intervalMax {
		errs = append(errs, fmt.Sprintf("轮询间隔必须在 [%d, %d] 分钟内", intervalMin, intervalMax))
		iv = intervalMin
	}
	nc.IntervalMinutes = iv

	nc.DNS.Mode = g.gDNSMode.Selected
	servers := splitLines(g.gDOH.Text)
	nc.DNS.DOHServers = servers
	if len(servers) == 0 {
		errs = append(errs, "DoH 服务器列表不能为空")
	}
	for _, s := range servers {
		if !validHTTPSURL(s) {
			errs = append(errs, fmt.Sprintf("DoH 服务器必须是 https URL: %q", s))
		}
	}
	if err := config.ValidateGlobal(&nc); err != nil {
		errs = append(errs, err.Error())
	}
	nc.DNS.IPVersion = g.gIPVer.Selected
	return nc, errs
}

// splitLines 按换行拆分非空行并去首尾空白。
func splitLines(s string) []string {
	var out []string
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" {
			out = append(out, line)
		}
	}
	return out
}

// validateInterval 输入即时校验（规则 1）。
func validateInterval(s string) error {
	if s == "" {
		return nil // 空由提交兜底
	}
	n, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		return fmt.Errorf("必须为整数分钟")
	}
	if n < intervalMin || n > intervalMax {
		return fmt.Errorf("范围 [%d, %d]", intervalMin, intervalMax)
	}
	return nil
}

// validHTTPSURL 判定合法 https URL（规则 9；与 config 包内实现同判据，避免导出）。
func validHTTPSURL(s string) bool {
	u, err := url.Parse(s)
	return err == nil && u.Scheme == "https" && u.Host != ""
}

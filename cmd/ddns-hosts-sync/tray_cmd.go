package main

import (
	"errors"
	"flag"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"time"

	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/app"
	"fyne.io/fyne/v2/dialog"
	"fyne.io/fyne/v2/widget"

	"github.com/heliopause/ddns-hosts-sync/assets"
	"github.com/heliopause/ddns-hosts-sync/internal/gui"
	"github.com/heliopause/ddns-hosts-sync/internal/platform"
	"github.com/heliopause/ddns-hosts-sync/internal/tray"
)

// fyneAppID fyne 应用唯一标识（窗口/通知 appid 用，非产品 brand）。
const fyneAppID = "com.ddns-hosts-sync"

// 单实例锁文件放置于 config 同目录：state/ 对 Users 只读（DSD §5.1 ACL），
// 普通用户在 config/（Users:M）下创建 O_EXCL 锁文件才可行。
func trayLockPath(p platform.Platform) string {
	return filepath.Join(filepath.Dir(p.ConfigPath()), "tray.lock")
}

// runTray 启动托盘 UI（DSD §3.1：用户态常驻；HKCU Run 登录自启）。
//
// 启动顺序遵循 fyne 语义：
//   - 主 goroutine 运行 fyne 事件循环（app.Run，GUI 主循环约定）；
//   - goroutine 运行托盘循环（tray.Run → fyne.io/systray 独立消息循环），
//     托盘回调（菜单/单击）经队列串行化，窗口动作经 fyne.Do 入主循环；
//   - 配置窗口启动时不自动显示（静默驻留托盘，单击图标/菜单打开）；
//   - 关闭配置窗口 = 隐藏（后台任务不受影响），托盘"退出"才整体退出。
//
// 退出码：0 = 正常退出（含"程序已在运行"的软退出）；1 = 托盘初始化错误。
func runTray(args []string) int {
	fs := flag.NewFlagSet("tray", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	if err := fs.Parse(args); err != nil {
		if err == flag.ErrHelp {
			return 0
		}
		return 1
	}
	if fs.NArg() > 0 {
		fmt.Fprintf(os.Stderr, "tray: 多余的参数: %v\n", fs.Args())
		return 1
	}

	p := platform.New()

	// ── fyne 应用与配置窗口（主 goroutine 构建，不在此启动循环）──
	fa := app.NewWithID(fyneAppID)
	if b, ok := assets.AppIcon(); ok {
		fa.SetIcon(fyne.NewStaticResource("app-icon", b))
	}
	g := gui.New(gui.Deps{
		App:         fa,
		ConfigPath:  p.ConfigPath(),
		StateDir:    p.StateDir(),
		TriggerPath: p.TriggerPath(),
		LogPath:     p.LogPath(),
		TriggerNow:  func() { p.TriggerTask(platform.TaskName) }, // 尽力触发，失败静默（DSD §4.5）
		OpenLogsDir: openLogsDir,
	})
	// 关闭配置窗口 = 仅隐藏（托盘常驻，后台自动同步不受影响）。
	// 首次关闭给一句提示（ARCH §3.2 语义），其后静默隐藏。
	closeHintShown := false
	g.Window().SetCloseIntercept(func() {
		if !closeHintShown {
			closeHintShown = true
			dialog.ShowInformation("提示", "仅关闭本窗口，后台自动同步不受影响。", g.Window())
		}
		g.Window().Hide()
	})
	g.StartPolling()

	// ── 托盘（独立循环；回调窗口动作统一 fyne.Do 入主 goroutine）──
	tr := tray.New(tray.Deps{
		LockPath:    trayLockPath(p),
		ConfigPath:  p.ConfigPath(),
		StatusPath:  filepath.Join(p.StateDir(), "status.json"),
		TriggerPath: p.TriggerPath(), // B2：config\ 下 trigger.json（Users M 可写）
		OpenConfig: func() {
			fyne.Do(func() { g.ShowOrFocus() })
		},
		OpenLogsDir: openLogsDir,
		TriggerTask: func() { p.TriggerTask(platform.TaskName) },
		OnQuit: func() {
			fyne.Do(func() {
				g.StopPolling()
				fa.Quit()
			})
		},
	})

	errCh := make(chan error, 1)
	go func() {
		err := tr.Run()
		if err == nil {
			return
		}
		if errors.Is(err, tray.ErrAlreadyRunning) {
			// 后到者提示"程序已在运行"并自动退出（DSD §3.4 多实例行）。
			fyne.Do(func() {
				w := fa.NewWindow("ddns-hosts-sync")
				w.SetContent(widget.NewLabel("程序已在运行，请查看系统托盘图标。"))
				w.Show()
				time.AfterFunc(3*time.Second, fa.Quit) // 短暂展示后自退
			})
			return
		}
		fmt.Fprintf(os.Stderr, "tray: %v\n", err)
		fa.Quit()
	}()

	// 主 goroutine：fyne 事件循环（阻塞直到 fa.Quit()）。
	fa.Run()

	// 收尾：优先取托盘结果（ErrAlreadyRunning 视为软退出，提示已弹出）。
	select {
	case err := <-errCh:
		if errors.Is(err, tray.ErrAlreadyRunning) {
			return 0
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "tray: %v\n", err)
			return 1
		}
	default:
	}
	g.StopPolling()
	return 0
}

// openLogsDir 在系统文件管理器中定位日志目录（并选中日志文件）。
// 失败静默（无 window manager / explorer 不可用等）：故障不阻断托盘主流程。
func openLogsDir() {
	logPath := platform.New().LogPath()
	dir := filepath.Dir(logPath)
	var cmd *exec.Cmd
	switch runtime.GOOS {
	case "windows":
		// /select, 直接定位文件；参数整体为路径（含反斜杠）。
		cmd = exec.Command("explorer", "/select,"+logPath)
	case "darwin":
		cmd = exec.Command("open", "-R", logPath) // 在 Finder 中选中文件
	default:
		cmd = exec.Command("xdg-open", dir)
	}
	_ = cmd.Start() // 异步打开；失败静默
}

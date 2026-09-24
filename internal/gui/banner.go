package gui

import (
	"fyne.io/fyne/v2"
	"fyne.io/fyne/v2/theme"
	"fyne.io/fyne/v2/widget"
)

// bannerKind 状态栏横幅类型（决定主题色，DSD §4.1 "横幅(warn/err，Appearance 变色)"）。
type bannerKind int

const (
	bannerOK bannerKind = iota
	bannerWarn
	bannerErr
)

// bannerColorName 横幅语义主题色（跟随深浅色模式）。
var bannerColorName = map[bannerKind]fyne.ThemeColorName{
	bannerOK:   theme.ColorNameSuccess,
	bannerWarn: theme.ColorNameWarning,
	bannerErr:  theme.ColorNameError,
}

// showBanner 更新状态栏横幅文本与颜色。可从任意 goroutine 调用
// （内部经 fyne.Do 入主循环，test driver 下同步直通）。
func (g *GUI) showBanner(text string, kind bannerKind) {
	if g.banner == nil {
		return
	}
	fyne.Do(func() {
		g.banner.Segments = []widget.RichTextSegment{
			&widget.TextSegment{
				Style: widget.RichTextStyle{ColorName: bannerColorName[kind]},
				Text:  text,
			},
		}
		g.banner.Refresh()
	})
}

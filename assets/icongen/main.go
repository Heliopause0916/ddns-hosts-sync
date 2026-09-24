// Command icongen 生成托盘四色占位图标（M2b-2 占位资产，M3 打磨正式图标）。
//
// 输出 64x64 PNG 到 ../icons/{gray,green,yellow,red}.png。
// 图标样式：同色系圆形底（浅内圈）+ 居中标记，四色区分状态；
// 占位阶段仅用几何图形（image/png 纯标准库，无第三方依赖）。
//
// 用法： go run ./assets/icongen
package main

import (
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"log"
	"os"
	"path/filepath"
)

const size = 64

// statusColor 四种托盘状态主色（语义：灰=未运行/停用，绿=正常，黄=告警，红=故障）。
var statusColor = map[string]color.RGBA{
	"gray":   color.RGBA{R: 0x9e, G: 0x9e, B: 0x9e, A: 0xff},
	"green":  color.RGBA{R: 0x43, G: 0xa0, B: 0x47, A: 0xff},
	"yellow": color.RGBA{R: 0xff, G: 0xc1, B: 0x07, A: 0xff}, // #FFC107
	"red":    color.RGBA{R: 0xe5, G: 0x39, B: 0x35, A: 0xff},
}

// statusMark 中央标记颜色（白色），占位阶段统一白点。
var markColor = color.RGBA{R: 0xff, G: 0xff, B: 0xff, A: 0xff}

func main() {
	outDir, err := filepath.Abs(filepath.Join(".", "assets", "icons"))
	if err != nil {
		log.Fatal(err)
	}
	// 兼容从仓库根或 assets/ 目录运行的两种路径。
	if _, err := os.Stat(filepath.Join(outDir, "gray.png")); err == nil {
		// 已存在目标目录的校验在下方统一完成
	} else if fi, err := os.Stat(outDir); err == nil && fi.IsDir() {
		// 从 assets/ 运行：outDir 则是 assets/assets/icons
		outDir = filepath.Join(filepath.Dir(outDir), "icons")
	}

	log.Println("图标输出目录:", outDir)
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		log.Fatal(err)
	}

	for name, c := range statusColor {
		img := render(c)
		path := filepath.Join(outDir, name+".png")
		f, err := os.Create(path)
		if err != nil {
			log.Fatalf("%s: %v", name, err)
		}
		if err := png.Encode(f, img); err != nil {
			f.Close()
			log.Fatalf("%s: %v", name, err)
		}
		if err := f.Close(); err != nil {
			log.Fatal(err)
		}
		log.Printf("生成 %s (%dx%d)", path, size, size)
	}
}

// render 绘制单个图标：浅色底圆 + 主色圆环 + 中心白色圆点。
func render(base color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, size, size))
	draw.Draw(img, img.Bounds(), &image.Uniform{C: color.RGBA{R: 0, G: 0, B: 0, A: 0}}, image.Point{}, draw.Src)

	// 浅色底圆（直径 60，居中）：放大视觉辨识度。
	pale := color.RGBA{
		R: uint8(min(255, int(base.R)+(0xff-int(base.R))*2/3)),
		G: uint8(min(255, int(base.G)+(0xff-int(base.G))*2/3)),
		B: uint8(min(255, int(base.B)+(0xff-int(base.B))*2/3)),
		A: 0xff,
	}
	fillCircle(img, 32, 32, 30, pale)
	// 主色环（外径 30，内 24 的环）。
	fillRing(img, 32, 32, 30, 24, base)
	// 中心白点（半径 10）。
	fillCircle(img, 32, 32, 10, markColor)
	return img
}

func fillCircle(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	for y := cy - r; y <= cy+r; y++ {
		for x := cx - r; x <= cx+r; x++ {
			if x < 0 || y < 0 || x >= size || y >= size {
				continue
			}
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r*r {
				img.Set(x, y, c)
			}
		}
	}
}

func fillRing(img *image.RGBA, cx, cy, rOuter, rInner int, c color.RGBA) {
	for y := cy - rOuter; y <= cy+rOuter; y++ {
		for x := cx - rOuter; x <= cx+rOuter; x++ {
			if x < 0 || y < 0 || x >= size || y >= size {
				continue
			}
			dx, dy := x-cx, y-cy
			d2 := dx*dx + dy*dy
			if d2 <= rOuter*rOuter && d2 >= rInner*rInner {
				img.Set(x, y, c)
			}
		}
	}
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}

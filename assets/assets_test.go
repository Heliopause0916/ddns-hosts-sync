package assets

import (
	"bytes"
	"image/png"
	"testing"
)

func TestIconsAllPresent(t *testing.T) {
	for _, name := range AllIconNames() {
		b, err := Icon(name)
		if err != nil {
			t.Fatalf("图标 %s 缺失: %v", name, err)
		}
		if len(b) == 0 {
			t.Fatalf("图标 %s 为空", name)
		}
		// 解码验证为合法 PNG（syzstray 在 Windows 侧需 PNG 字节）。
		img, err := png.Decode(bytes.NewReader(b))
		if err != nil {
			t.Fatalf("图标 %s 不是合法 PNG: %v", name, err)
		}
		bounds := img.Bounds()
		if bounds.Dx() == 0 || bounds.Dy() == 0 {
			t.Fatalf("图标 %s 尺寸非法: %v", name, bounds)
		}
	}
}

func TestIconUnknown(t *testing.T) {
	if _, err := Icon("nonexistent"); err == nil {
		t.Fatal("未知图标应返回错误")
	}
}

func TestAppIcon(t *testing.T) {
	b, ok := AppIcon()
	if !ok || len(b) == 0 {
		t.Fatal("AppIcon 应可用")
	}
}

func TestIconNamesStable(t *testing.T) {
	got := AllIconNames()
	want := []string{"gray", "green", "yellow", "red"}
	if len(got) != len(want) {
		t.Fatalf("图标名数量 = %d, want %d", len(got), len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("图标序 = %v, want %v", got, want)
		}
	}
}

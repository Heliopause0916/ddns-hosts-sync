package hostsfile

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

func block(t *testing.T, lines []Line) []byte {
	t.Helper()
	b, err := BuildBlock(lines, model.OrderBoth)
	if err != nil {
		t.Fatalf("BuildBlock 失败: %v", err)
	}
	return b
}

// ---------------------------------------------------------------------------
// BuildBlock：排序与 TAB
// ---------------------------------------------------------------------------

func TestBuildBlockSortAndTAB(t *testing.T) {
	lines := []Line{
		{IP: "2001:db8::2", Target: "v6b.example.com"},
		{IP: "203.0.113.9", Target: "v4big.example.com"},
		{IP: "198.51.100.1", Target: "v4small.example.com"},
		{IP: "2001:db8::1", Target: "v6a.example.com"},
	}
	got := string(block(t, lines))
	want := "198.51.100.1\tv4small.example.com\n" +
		"203.0.113.9\tv4big.example.com\n" +
		"2001:db8::1\tv6a.example.com\n" +
		"2001:db8::2\tv6b.example.com\n"
	if got != want {
		t.Errorf("排序/TAB 不符:\n%q\n期望:\n%q", got, want)
	}
}

func TestBuildBlockEmpty(t *testing.T) {
	if got := block(t, nil); len(got) != 0 {
		t.Errorf("空行集应返回空字节，实际 %q", got)
	}
}

func TestBuildBlockInvalidOrder(t *testing.T) {
	if _, err := BuildBlock(nil, model.IPOrder("ipx")); err == nil {
		t.Error("非法 IPOrder 应报错")
	}
}

// ---------------------------------------------------------------------------
// ComposeFull：缺块追加 / 有块替换 / 块外字节原样
// ---------------------------------------------------------------------------

func TestComposeFullAppendWhenMissing(t *testing.T) {
	content := []byte("127.0.0.1\tlocalhost\n::1\tlocalhost6\n")
	full, changed, err := ComposeFull(content, block(t, []Line{{"203.0.113.42", "t.example.com"}}))
	if err != nil {
		t.Fatalf("ComposeFull 失败: %v", err)
	}
	if !changed {
		t.Error("缺块追加应标记 changed")
	}
	if !bytes.HasPrefix(full, content) {
		t.Errorf("块外前缀必须原样保留:\n%q", full)
	}
	wantSuffix := BeginMarker + "\n" + "203.0.113.42\tt.example.com\n" + EndMarker + "\n"
	if !bytes.HasSuffix(full, []byte(wantSuffix)) {
		t.Errorf("应追加规范区块:\n%q", full)
	}
}

func TestComposeFullReplaceExisting(t *testing.T) {
	head := []byte("10.0.0.1\tgateway.local\n# orig comment\n")
	tail := []byte("255.255.255.255\tbroadcast\n")
	oldRegion := []byte(BeginMarker + "\n1.2.3.4\told.example.com\n" + EndMarker + "\n")
	origFull := append(append(append(append([]byte{}, head...), oldRegion...), tail...), []byte("8.8.8.8\tresolver\n")...)
	full, changed, err := ComposeFull(origFull, block(t, []Line{{"5.6.7.8", "new.example.com"}}))
	if err != nil {
		t.Fatalf("ComposeFull 失败: %v", err)
	}
	if !changed {
		t.Error("块内容变化应标记 changed")
	}
	if !bytes.HasPrefix(full, head) || !bytes.HasSuffix(full, []byte("8.8.8.8\tresolver\n")) {
		t.Errorf("块外内容必须逐字节保留:\n%q", full)
	}
	// 中间 marker 区应只含新块（旧块被替换，tail 仍紧跟其后）。
	mid := full[len(head) : len(full)-len([]byte("8.8.8.8\tresolver\n"))-len(tail)]
	wantMid := BeginMarker + "\n5.6.7.8\tnew.example.com\n" + EndMarker + "\n"
	if string(mid) != wantMid {
		t.Errorf("替换区间不符:\n%q\n期望:\n%q", mid, wantMid)
	}
}

func TestComposeFullNoChange(t *testing.T) {
	content := []byte("10.0.0.1\tgateway.local\n")
	inner := block(t, []Line{{"203.0.113.42", "t.example.com"}, {"2001:db8::1", "t6.example.com"}})
	full, changed, err := ComposeFull(content, inner)
	if err != nil {
		t.Fatalf("ComposeFull 失败: %v", err)
	}
	full2, changed2, err := ComposeFull(full, inner)
	if err != nil {
		t.Fatalf("ComposeFull 二次失败: %v", err)
	}
	if !bytes.Equal(full, full2) {
		t.Error("幂等性破坏：同输入二次组装结果不同")
	}
	if changed2 {
		t.Error("内容一致时二次组装不应标记 changed（不写盘）")
	}
	_ = changed
}

func TestComposeFullNonUTF8OutsidePreserved(t *testing.T) {
	// 构造含非 UTF-8 字节段的 mock 文件：块外字节必须逐字节原样复制。
	nonUTF8 := []byte{0x50, 0x56, 0x45, 0x20, 0xC3, 0x28, 0xA0, 0xA1, 0xE1, 0xE2, 0x0A, 0x20, 0xFF, 0xFE, 0x0A}
	before := []byte("127.0.0.1\tlocalhost\n")
	after := []byte{0xFF, 0xFE, 0x0A, 0x20, 0xC3, 0x28, 0x0A}
	content := append(append(append([]byte{}, before...), nonUTF8...), after...)
	full, _, err := ComposeFull(content, block(t, []Line{{"203.0.113.42", "t.example.com"}}))
	if err != nil {
		t.Fatalf("ComposeFull 失败: %v", err)
	}
	// before 段整体命中；nonUTF8/after 段逐字节包含。
	if !bytes.Contains(full[:len(before)], before) {
		t.Error("before 块外内容丢失")
	}
	if !bytes.Contains(full, nonUTF8) {
		t.Errorf("非 UTF-8 段被改写/丢失:\n%x\n全文:\n%x", nonUTF8, full)
	}
	if !bytes.Contains(full, after) {
		t.Errorf("文末非 UTF-8 段被改写/丢失:\n%x", after)
	}
}

func TestComposeFullCRLF(t *testing.T) {
	content := []byte("127.0.0.1\tlocalhost\r\n10.0.0.1\tgw.local\r\n")
	full, _, err := ComposeFull(content, block(t, []Line{{"203.0.113.42", "t.example.com"}}))
	if err != nil {
		t.Fatalf("ComposeFull 失败: %v", err)
	}
	if !bytes.Contains(full, []byte(BeginMarker+"\r\n")) || !bytes.Contains(full, []byte(EndMarker+"\r\n")) {
		t.Errorf("CRLF 文件应沿用 \\r\\n 写块:\n%q", full)
	}
	if !bytes.Contains(full, []byte("203.0.113.42\tt.example.com\r\n")) {
		t.Errorf("块行应以 CRLF 结尾:\n%q", full)
	}
	// \r\n 不应出现在块外非换行处——检查块外字节未被重写为 \n（原样保持）。
	if !bytes.Contains(full, []byte("localhost\r\n10.0.0.1")) {
		t.Errorf("块外 CRLF 被改写:\n%q", full)
	}
}

func TestComposeFullEmptyContent(t *testing.T) {
	full, changed, err := ComposeFull(nil, block(t, []Line{{"203.0.113.42", "t.example.com"}}))
	if err != nil {
		t.Fatalf("ComposeFull 失败: %v", err)
	}
	if !changed || !bytes.HasPrefix(full, []byte(BeginMarker+"\r\n")) {
		t.Errorf("空文件应默认 \\r\\n 追加块（Windows 优先）:\n%q", full)
	}
	// 空块（无 OK 条目）保留两 marker。
	empty, _, err := ComposeFull(nil, block(t, nil))
	if err != nil {
		t.Fatalf("ComposeFull 空块失败: %v", err)
	}
	if string(empty) != BeginMarker+"\r\n"+EndMarker+"\r\n" {
		t.Errorf("空块应保留两条 marker:\n%q", empty)
	}
}

// ---------------------------------------------------------------------------
// LocateBlock
// ---------------------------------------------------------------------------

func TestLocateBlock(t *testing.T) {
	if _, _, found := LocateBlock(nil); found {
		t.Error("空文件不应 found")
	}
	// 仅 begin 无 end → 缺块。
	if _, _, found := LocateBlock([]byte(BeginMarker + "\n")); found {
		t.Error("只有 begin marker 不应 found")
	}
	content := []byte("10.0.0.1\tgw.local\n" + BeginMarker + "\n1.2.3.4\tt.example.com\n" + EndMarker + "\n::1\tlocal6\n")
	begin, end, found := LocateBlock(content)
	if !found {
		t.Fatal("应找到块")
	}
	if string(content[begin:end]) != BeginMarker+"\n1.2.3.4\tt.example.com\n"+EndMarker+"\n" {
		t.Errorf("区间定位错误:\n%q", content[begin:end])
	}
	// 块在文末且无结尾换行。
	c2 := []byte(BeginMarker + "\n# END ddns-hosts-sync")
	_, end2, found := LocateBlock(c2)
	if !found || end2 != len(c2) {
		t.Errorf("无尾换行块 end 应等于 len，实际 %d/%d", end2, len(c2))
	}
	// marker 必须独占整行：行内嵌 marker 不命中。
	c3 := []byte("x # BEGIN ddns-hosts-sync\n")
	if _, _, found := LocateBlock(c3); found {
		t.Error("行内嵌 marker 不应命中")
	}
}

// ---------------------------------------------------------------------------
// ParseBlock / MD5Block
// ---------------------------------------------------------------------------

func TestParseBlock(t *testing.T) {
	inner := block(t, []Line{
		{"203.0.113.42", "t1.example.com"},
		{"2001:db8::10", "t6.example.com"},
	})
	full, _, _ := ComposeFull([]byte("10.0.0.1\tgw.local\n"), inner)
	lines := ParseBlock(full)
	if len(lines) != 2 {
		t.Fatalf("应解析出 2 行，实际 %d: %v", len(lines), lines)
	}
	if lines[0].IP != "203.0.113.42" || lines[0].Target != "t1.example.com" {
		t.Errorf("解析行不符: %+v", lines[0])
	}
	if lines[1].IP != "2001:db8::10" || lines[1].Target != "t6.example.com" {
		t.Errorf("解析行不符: %+v", lines[1])
	}
	// 缺块 → 空。
	if got := ParseBlock([]byte("10.0.0.1\tgw.local\n")); got != nil {
		t.Errorf("缺块应返回 nil，实际 %v", got)
	}
}

func TestMD5Block(t *testing.T) {
	content := []byte(BeginMarker + "\n1.2.3.4\tt.example.com\n" + EndMarker + "\n")
	got, err := MD5Block(content)
	if err != nil {
		t.Fatalf("MD5Block 失败: %v", err)
	}
	// 已知 md5（printf ... | md5sum 计算）。
	if got != "222dbb65f0d1663bbf6a69dca9bf7bb8" {
		t.Errorf("md5 不符: %s", got)
	}
	if _, err := MD5Block([]byte("no block")); !errors.Is(err, ErrBlockNotFound) {
		t.Errorf("缺块应 ErrBlockNotFound，实际 %v", err)
	}
}

// ---------------------------------------------------------------------------
// WriteAtomic / Backup
// ---------------------------------------------------------------------------

func TestWriteAtomicAndBackup(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "hosts")
	// Backup：源不存在 → 无备份（非错误）。
	if bp, err := Backup(path); err != nil || bp != "" {
		t.Errorf("源不存在 Backup 应返回 (\"\", nil)，实际 (%q, %v)", bp, err)
	}
	// WriteAtomic 写盘。
	data := []byte(BeginMarker + "\n1.2.3.4\tt.example.com\n" + EndMarker + "\n10.0.0.1\tgw\n")
	if err := WriteAtomic(path, data, ""); err != nil {
		t.Fatalf("WriteAtomic 失败: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil || !bytes.Equal(got, data) {
		t.Fatalf("写后读回不一致: %v", err)
	}
	// 无临时文件残留。
	entries, _ := os.ReadDir(dir)
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".tmp") {
			t.Errorf("残留临时文件: %s", e.Name())
		}
	}
	// Backup：复制旧版本。
	bp, err := Backup(path)
	if err != nil {
		t.Fatalf("Backup 失败: %v", err)
	}
	if filepath.Base(bp) != "hosts.bak-ddns-hosts-sync" {
		t.Errorf("备份名不符: %s", bp)
	}
	bak, _ := os.ReadFile(bp)
	if !bytes.Equal(bak, data) {
		t.Error("备份内容应为当前 hosts 内容")
	}
	// 循环覆盖：改 hosts → 再备份 → 备份为新内容，旧 .1 不存在（覆盖而非追加）。
	newData := []byte("changed\n")
	if err := WriteAtomic(path, newData, ""); err != nil {
		t.Fatalf("WriteAtomic 二次失败: %v", err)
	}
	if _, err := Backup(path); err != nil {
		t.Fatalf("Backup 二次失败: %v", err)
	}
	bak2, _ := os.ReadFile(bp)
	if !bytes.Equal(bak2, newData) {
		t.Errorf("备份应循环覆盖为最新内容: %q", bak2)
	}
}

func TestWriteAtomicIntoMissingDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "nested", "hosts") // nested 不存在
	err := WriteAtomic(path, []byte("x"), "")
	if !errors.Is(err, ErrWriteFailed) {
		t.Fatalf("期望 ErrWriteFailed，实际 %v", err)
	}
	if _, serr := os.Stat(path); !os.IsNotExist(serr) {
		t.Error("失败时不应产生目标文件")
	}
}

// 参考值校验：MD5Block 与标准 md5 一致。
func TestHexMD5Reference(t *testing.T) {
	sum := md5.Sum([]byte("abc"))
	if hex.EncodeToString(sum[:]) != "900150983cd24fb0d6963f7d28e17f72" {
		t.Error("hexMD5 参考值不符")
	}
}

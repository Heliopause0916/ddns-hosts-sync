// Package hostsfile 实现 hosts 标记块的字节级操作（DSD §2.2、§5.3）。
//
// 关键设计：Read/LocateBlock/ComposeFull 全程以 []byte 操作，marker 用字节锚定，
// 块外内容逐字节原样复制，绝不 decode/re-encode——规避 Windows hosts 非 UTF-8
// 编码问题（DSD §7.2）。换行符沿用文件现有（detect 首个换行为 \r\n 或 \n，
// 无则默认 \r\n，Windows 优先）。
package hostsfile

import (
	"bytes"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"net"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// 块边界 marker（DSD §2.2）。marker 独占整行、无后缀注释，保证字节锚点唯一。
const (
	BeginMarker = "# BEGIN ddns-hosts-sync"
	EndMarker   = "# END ddns-hosts-sync"
)

var (
	// ErrWriteFailed 写盘失败（DSD §2.2，包装底层错误）。
	ErrWriteFailed = errors.New("hostsfile: write failed")
	// ErrBlockNotFound 文件中不存在完整 marker 对（MD5Block 用）。
	ErrBlockNotFound = errors.New("hostsfile: block not found")
)

// Line 一条待写记录（已校验）：IP<TAB>Target。
type Line struct {
	IP     string
	Target string
}

// WriteReport 一次写盘的结果摘要（DSD §2.2）。
type WriteReport struct {
	Changed    bool   // 块内容是否变化（changed=false 不写盘）
	ContentMD5 string // 写后块 md5
	EOL        string // 检出的换行符（"\r\n"|"\n"），写盘沿用
}

// Read 读取文件原始字节；不存在返回 os.ErrNotExist（errors.Is 可直接判定）。
func Read(path string) (content []byte, err error) {
	content, err = os.ReadFile(path)
	return content, err // 原样透传 os.ErrNotExist
}

// LocateBlock 返回 marker 块字节区间 [begin, end)：begin 为 BeginMarker 行首，
// end 为 EndMarker 行尾（含其换行符，若存在）。found=false 表示缺块
// （marker 缺一或顺序异常）。
func LocateBlock(content []byte) (begin, end int, found bool) {
	n := len(content)
	b := -1
	for i := 0; i < n; {
		j := i
		for j < n && content[j] != '\n' {
			j++
		}
		lineEnd := j
		if lineEnd > i && content[lineEnd-1] == '\r' {
			lineEnd--
		}
		line := content[i:lineEnd]
		switch {
		case b < 0 && bytes.Equal(line, []byte(BeginMarker)):
			b = i
		case b >= 0 && bytes.Equal(line, []byte(EndMarker)):
			e := j
			if j < n && content[j] == '\n' {
				e = j + 1
			}
			return b, e, true
		}
		if j >= n {
			break
		}
		i = j + 1
	}
	return 0, 0, false
}

// MD5Block 对当前文件中标记块字节区间计算 md5（hex）。块缺失返回 ErrBlockNotFound。
func MD5Block(content []byte) (string, error) {
	begin, end, found := LocateBlock(content)
	if !found {
		return "", ErrBlockNotFound
	}
	return hexMD5(content[begin:end])
}

// BuildBlock 将记录行组装为规范化块文本（DSD §5.3）：
//
//	IP<TAB>Target 每行一条；排序：IPv4 字典序在前、IPv6 字典序在后
//	（both 模式下 A 行恒在 AAAA 行前）；返回不含 markers 的内层文本，
//	空行集返回空字节（空块保留两 marker 由 BlockText/ComposeFull 承担）。
//
// order 为 DSD 签名入参（model.IPOrder），排序规则固定为上述 §5.3。
func BuildBlock(lines []Line, order model.IPOrder) ([]byte, error) {
	switch order {
	case model.OrderIPv4, model.OrderIPv6, model.OrderBoth:
	default:
		return nil, fmt.Errorf("hostsfile: 非法 IPOrder %q", order)
	}
	sorted := make([]Line, 0, len(lines))
	for _, ln := range lines {
		if net.ParseIP(ln.IP) == nil {
			continue // 防御：非 IP 行不进入托管块
		}
		sorted = append(sorted, Line{IP: normalizeIP(ln.IP), Target: ln.Target})
	}
	sort.SliceStable(sorted, func(a, b int) bool {
		fa, fb := ipFamily(sorted[a].IP), ipFamily(sorted[b].IP)
		if fa != fb {
			return fa < fb // v4(0) 恒在 v6(1) 前
		}
		return sortKey(sorted[a].IP) < sortKey(sorted[b].IP)
	})
	var buf bytes.Buffer
	for _, ln := range sorted {
		buf.WriteString(ln.IP)
		buf.WriteByte('\t')
		buf.WriteString(ln.Target)
		buf.WriteByte('\n')
	}
	return buf.Bytes(), nil
}

// BlockText 由内层行文本组装规范化区块全文（含两条 marker，EOL 沿用给定值）。
// 空内层（inner 为空）时仍保留两 marker，保证 present=true 可检测（DSD §5.3）。
func BlockText(inner []byte, eol string) []byte {
	var buf bytes.Buffer
	buf.WriteString(BeginMarker)
	buf.WriteString(eol)
	if len(inner) > 0 {
		// 内层行以 \n 结尾，逐字节替换为目标 EOL（块外字节不参与）。
		buf.Write(bytes.ReplaceAll(inner, []byte("\n"), []byte(eol)))
	}
	buf.WriteString(EndMarker)
	buf.WriteString(eol)
	return buf.Bytes()
}

// ComposeFull 将 content 与期望块合并为新全文：
//   - 缺块：追加文末（content 非空且不以换行结尾时先补 EOL）；
//   - 有块：替换 [begin,end) 区间；
//   - 块外字节原样保留，绝不 decode/re-encode。
//
// 返回 changed：full 是否与原文逐字节不同（相同则不写盘）。
func ComposeFull(content []byte, block []byte) (full []byte, changed bool, err error) {
	eol := DetectEOL(content)
	region := BlockText(block, eol)
	if begin, end, found := LocateBlock(content); found {
		full = make([]byte, 0, len(content)-(end-begin)+len(region))
		full = append(full, content[:begin]...)
		full = append(full, region...)
		full = append(full, content[end:]...)
	} else {
		switch {
		case len(content) == 0:
			full = region
		case content[len(content)-1] == '\n':
			full = make([]byte, 0, len(content)+len(region))
			full = append(full, content...)
			full = append(full, region...)
		default:
			full = make([]byte, 0, len(content)+len(eol)+len(region))
			full = append(full, content...)
			full = append(full, eol...)
			full = append(full, region...)
		}
	}
	return full, !bytes.Equal(full, content), nil
}

// RemoveBlock 删除全文中整个标记块（含两条 marker，uninstall 还原 hosts 用，
// ARCHITECTURE §4.3）。块缺失返回 (content 原样, false, nil)；块外字节原样
// 保留——若删除后全文为空或仅剩空行，调用方可按空文件处理。
func RemoveBlock(content []byte) (rest []byte, changed bool) {
	begin, end, found := LocateBlock(content)
	if !found {
		return content, false
	}
	rest = make([]byte, 0, len(content)-(end-begin))
	rest = append(rest, content[:begin]...)
	rest = append(rest, content[end:]...)
	return rest, true
}

// WriteAtomic 原子写：同目录 temp（O_EXCL）→ fsync → rename 覆盖。
// eol 参数为 DSD 签名保留（全文已含目标 EOL，写盘不做任何转换）。
// 失败时清理临时文件并以 ErrWriteFailed 包装。
func WriteAtomic(path string, full []byte, eol string) error {
	dir := filepath.Dir(path)
	tmp := filepath.Join(dir, fmt.Sprintf(".%s.%d.%d.tmp", filepath.Base(path), os.Getpid(), time.Now().UnixNano()))
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o644)
	if err != nil {
		return fmt.Errorf("%w: 创建临时文件: %v", ErrWriteFailed, err)
	}
	fail := func(step string, e error) error {
		_ = f.Close()
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: %s: %v", ErrWriteFailed, step, e)
	}
	if _, err := f.Write(full); err != nil {
		return fail("写入", err)
	}
	if err := f.Sync(); err != nil {
		return fail("fsync", err)
	}
	if err := f.Close(); err != nil {
		return fail("关闭", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		return fmt.Errorf("%w: rename: %v", ErrWriteFailed, err)
	}
	syncDir(dir) // rename 后父目录 fsync（POSIX 持久性）
	return nil
}

// syncDir 对目录做 fsync 保证 rename 产生的目录项变更落盘（POSIX 持久性）。
// Windows 无法 open 目录，忽略该步（尽力而为，错误不上报）。
func syncDir(dir string) {
	d, err := os.Open(dir)
	if err != nil {
		return
	}
	_ = d.Sync()
	_ = d.Close()
}

// Backup 将 path 当前内容复制为 `<basename>.bak-ddns-hosts-sync`（同目录，
// 循环覆盖旧备份），返回备份路径。源文件不存在时视为无备份可做，
// 返回 ("", nil)。
func Backup(path string) (backupPath string, err error) {
	content, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return "", nil
		}
		return "", err
	}
	backupPath = filepath.Join(filepath.Dir(path), filepath.Base(path)+".bak-ddns-hosts-sync")
	if werr := WriteAtomic(backupPath, content, DetectEOL(content)); werr != nil {
		return "", fmt.Errorf("备份到 %s 失败: %w", backupPath, werr)
	}
	return backupPath, nil
}

// DetectEOL 检测文件换行符：首个换行为 \r\n 或 \n；无换行（空/单行）默认 \r\n
// （Windows 优先，DSD §5.3）。
func DetectEOL(content []byte) string {
	idx := bytes.IndexByte(content, '\n')
	if idx < 0 {
		return "\r\n"
	}
	if idx > 0 && content[idx-1] == '\r' {
		return "\r\n"
	}
	return "\n"
}

// ParseBlock 解析 content 中标记块内的记录行（不含 markers），按 §5.3 正则
// ^(\S+)\s+(\S+)$ 解析；不可解析的行被跳过。IP 回到规范形态、Target 小写
// 归一（§5.3"Target 小写"）——手改大写 target 的块行与 config 小写值归一为
// 同一键，装配侧不会出现大小写双行。块缺失返回空切片（found=false 由
// LocateBlock 另行判定）。
func ParseBlock(content []byte) []Line {
	begin, end, found := LocateBlock(content)
	if !found {
		return nil
	}
	var out []Line
	// 跳过 BeginMarker 行，逐行扫描至 EndMarker。
	pos := begin + len(BeginMarker)
	if pos < len(content) && content[pos] == '\r' {
		pos++
	}
	if pos < len(content) && content[pos] == '\n' {
		pos++
	}
	for pos < end {
		j := pos
		for j < end && content[j] != '\n' {
			j++
		}
		lineEnd := j
		if lineEnd > pos && content[lineEnd-1] == '\r' {
			lineEnd--
		}
		line := bytes.TrimSpace(content[pos:lineEnd])
		if len(line) > 0 && !bytes.Contains(line, []byte("#")) {
			if m := linePattern.FindSubmatch(line); m != nil {
				ip, target := string(m[1]), string(m[2])
				if net.ParseIP(ip) != nil {
					out = append(out, Line{IP: normalizeIP(ip), Target: strings.ToLower(strings.TrimSpace(target))})
				}
			}
		}
		if j >= end {
			break
		}
		pos = j + 1
	}
	return out
}

// ---------------------------------------------------------------------------
// 工具
// ---------------------------------------------------------------------------

// linePattern 块内行格式（DSD §5.3）：^(\S+)\s+(\S+)$。
var linePattern = regexp.MustCompile(`^(\S+)\s+(\S+)$`)

func hexMD5(b []byte) (string, error) {
	sum := md5.Sum(b)
	return hex.EncodeToString(sum[:]), nil
}

func ipFamily(s string) int {
	if ip := net.ParseIP(s); ip != nil && ip.To4() != nil {
		return 0
	}
	return 1
}

func sortKey(s string) string {
	if ip := net.ParseIP(s); ip != nil {
		if ip.To4() != nil {
			return ip.To4().String()
		}
		return ip.To16().String()
	}
	return s
}

func normalizeIP(s string) string {
	if ip := net.ParseIP(s); ip != nil {
		if ip.To4() != nil {
			return ip.To4().String()
		}
		return ip.To16().String()
	}
	return s
}

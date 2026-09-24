// Package resolver 实现单条目 DNS 解析全链路（DSD §2.1，纯逻辑无平台差异）。
//
// 行为要点：
//   - DoH（application/dns-json，纯标准库）：doh_servers 依次尝试，每个 server
//     每级查询重试 1 次，单次尝试超时约 3s（受 cfg.TimeoutSec 全链路截止时间封顶）；
//   - CNAME 递归：循环跟随至 A/AAAA，visited 集合环检测，超过 max_cname_depth 报
//     ErrTooDeep；DoH 应答 Status=3（NXDOMAIN）直接短路返回 ErrNXDomain，不回退；
//   - 全部 DoH 通道失败后回退系统解析（net.Resolver）；mode=system 时直接系统解析；
//   - IP 排序按 DSD §5.3：IPv4 字典序在前、IPv6 字典序在后；
//   - ip_version 支持 ipv4/ipv6/both（只查 A / 只查 AAAA / A+AAAA 并行）。
package resolver

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"sort"
	"strings"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// Sentinel 错误（DSD §2.1）：errors.Is 为唯一判定手段；包装用 %w 保留链。
var (
	ErrCNAMELoop   = errors.New("resolver: cname loop detected")
	ErrTooDeep     = errors.New("resolver: cname chain exceeds max depth")
	ErrNXDomain    = errors.New("resolver: nxdomain")
	ErrTimeout     = errors.New("resolver: all resolution channels timed out")
	ErrInvalidName = errors.New("resolver: invalid hostname")
)

// ResolveResult 一次完整 CNAME 链解析的结果（DSD §2.1）。
type ResolveResult struct {
	IPs          []string  // 已排序（§5.3）：IPv4 字典序在前、IPv6 字典序在后
	Chain        []string  // 含 source 的完整链：["entry.ddns.net","relay.example.net"]
	FinalName    string    // 最终 A 记录的 owner（无 CNAME 时为 source）
	UsedFallback bool      // 最终经 system 回退命中
	ResolvedAt   time.Time // 解析完成时刻
}

// Resolve：单条目全链路入口（DSD §2.1）。DoH 列表依次查询 → 全败回退系统解析
// （mode=doh 时）；mode=system 时直接系统解析。失败返回非 nil error 且必须可
// 通过 errors.Is 匹配 sentinel。
func Resolve(source string, cfg model.DNSConfig) (*ResolveResult, error) {
	src := strings.ToLower(strings.TrimSpace(source))
	if !validName(src) {
		return nil, fmt.Errorf("resolve %q: %w", src, ErrInvalidName)
	}
	maxDepth := cfg.MaxCNAMEDepth
	if maxDepth <= 0 {
		maxDepth = maxCNAMEDepthDefault
	}
	timeoutSec := cfg.TimeoutSec
	if timeoutSec <= 0 {
		timeoutSec = timeoutSecDefault
	}
	deadline := time.Now().Add(time.Duration(timeoutSec) * time.Second)
	ctx, cancel := context.WithDeadline(context.Background(), deadline)
	defer cancel()

	qtypes := qtypesFor(cfg.IPVersion)
	name := src
	chain := []string{src}
	visited := map[string]bool{src: true}
	now := time.Now().UTC()

	// mode=system：直接系统解析（系统解析器内部跟随 CNAME 链）。
	if cfg.Mode == modeSystem {
		ips, final, err := querySystem(ctx, name, qtypes, deadline)
		if err != nil {
			return nil, wrapResolveErr(src, err, deadline)
		}
		if len(ips) == 0 {
			return nil, fmt.Errorf("resolve %q: %w (system 无结果)", src, ErrTimeout)
		}
		return &ResolveResult{IPs: sortIPs(ips), Chain: chain,
			FinalName: final, UsedFallback: false, ResolvedAt: now}, nil
	}

	// mode=doh：DoH 链式跟随，意外失败回退系统解析。
	hops := 0
	for {
		ips, cname, qerr := queryLevel(ctx, cfg.DOHServers, name, qtypes, deadline)
		if qerr != nil {
			if errors.Is(qerr, ErrNXDomain) {
				// NXDOMAIN 直接短路，不回退（DSD §2.1）。
				return nil, fmt.Errorf("resolve %q: %w", src, ErrNXDomain)
			}
			// 其它通道失败 → 对当前 name 回退系统解析。
			return dohFallbackSystem(src, ctx, name, qtypes, chain, now, qerr, deadline)
		}
		if qerr == nil && len(ips) == 0 && cname == "" {
			// NODATA（权威空答复）：视为通道失败，回退系统解析。
			return dohFallbackSystem(src, ctx, name, qtypes, chain, now, nil, deadline)
		}
		if len(ips) > 0 {
			return &ResolveResult{IPs: sortIPs(ips), Chain: chain,
				FinalName: name, UsedFallback: false, ResolvedAt: now}, nil
		}
		// 跟随 CNAME。
		hops++
		if hops > maxDepth {
			return nil, fmt.Errorf("resolve %q: %w (chain=%s)", src, ErrTooDeep, strings.Join(chain, "->"))
		}
		if visited[cname] {
			return nil, fmt.Errorf("resolve %q: %w (chain=%s)", src, ErrCNAMELoop,
				strings.Join(append(append([]string{}, chain...), cname), "->"))
		}
		visited[cname] = true
		chain = append(chain, cname)
		name = cname
	}
}

// dohFallbackSystem DoH 通道失败后的系统回退分支：成功 → UsedFallback=true；
// 失败 → 归一为 sentinel 错误。
func dohFallbackSystem(src string, ctx context.Context, name string, qtypes []string,
	chain []string, now time.Time, dohErr error, deadline time.Time) (*ResolveResult, error) {
	if time.Until(deadline) > 0 {
		ips, final, err := querySystem(ctx, name, qtypes, deadline)
		if err == nil && len(ips) > 0 {
			return &ResolveResult{IPs: sortIPs(ips), Chain: chain,
				FinalName: final, UsedFallback: true, ResolvedAt: now}, nil
		}
		if err != nil && errors.Is(err, ErrNXDomain) {
			return nil, fmt.Errorf("resolve %q: %w", src, ErrNXDomain)
		}
		if err == nil {
			err = errors.New("system 解析无结果")
		}
		if dohErr != nil {
			err = errors.Join(dohErr, err)
		}
		return nil, wrapResolveErr(src, err, deadline)
	}
	if dohErr != nil {
		return nil, wrapResolveErr(src, dohErr, deadline)
	}
	return nil, fmt.Errorf("resolve %q: %w", src, ErrTimeout)
}

// ---------------------------------------------------------------------------
// DoH 查询
// ---------------------------------------------------------------------------

// dohAnswer application/dns-json 应答结构（仅取必需字段）。
type dohAnswer struct {
	Status int `json:"Status"`
	Answer []struct {
		Name string `json:"name"`
		Type int    `json:"type"` // 1=A 5=CNAME 28=AAAA
		Data string `json:"data"`
	} `json:"Answer"`
}

type levelResult struct {
	ips   []string
	cname string
	err   error
}

// queryLevel 查询某一级 name：ipv4/ipv6 单类型顺序查询；both 时 A/AAAA 并行。
func queryLevel(ctx context.Context, servers []string, name string, qtypes []string, deadline time.Time) ([]string, string, error) {
	if len(qtypes) == 1 {
		return queryLevelType(ctx, servers, name, qtypes[0], deadline)
	}
	ch := make(chan levelResult, len(qtypes))
	for _, qt := range qtypes {
		go func(qt string) {
			i, c, e := queryLevelType(ctx, servers, name, qt, deadline)
			ch <- levelResult{ips: i, cname: c, err: e}
		}(qt)
	}
	// NXDOMAIN 优先级短路：任一通道命中即返回，**不等待另一通道完成**
	// （两通道是对同一 name 的并行查询，任一条 NXDOMAIN 即表名不存在，
	// 无需等慢通道——否则挂起的 A 通道会把 NXDOMAIN 拖到超时）。
	results := make([]levelResult, 0, len(qtypes))
	for len(results) < len(qtypes) {
		select {
		case r := <-ch:
			if errors.Is(r.err, ErrNXDomain) {
				return nil, "", r.err
			}
			results = append(results, r)
		case <-ctx.Done():
			// 全链路截止收束：剩余槽位补通道失败，防同时就绪时丢失已得数据；
			// 已到手的 ips/cname 照常参与下方合并。
			for len(results) < len(qtypes) {
				results = append(results, levelResult{err: fmt.Errorf("doh 全链路截止: %w", context.DeadlineExceeded)})
			}
		}
	}
	var errs []error
	var ipsOut, cnames []string
	anyData := false
	for _, r := range results {
		if r.err != nil {
			errs = append(errs, r.err)
			continue
		}
		ipsOut = append(ipsOut, r.ips...)
		if r.cname != "" {
			cnames = append(cnames, r.cname)
		}
		if len(r.ips) > 0 || r.cname != "" {
			anyData = true
		}
	}
	if len(cnames) > 0 {
		// 有 CNAME 记录则跟随（RFC：CNAME 名不得携带其它数据，防御性丢弃直连数据）。
		return nil, cnames[0], nil
	}
	if len(ipsOut) > 0 {
		return ipsOut, "", nil
	}
	if !anyData && len(errs) > 0 {
		return nil, "", errors.Join(errs...)
	}
	// 两通道均 NODATA：无错误、无数据，交由上层回退系统解析。
	return nil, "", nil
}

// queryLevelType 对单个查询类型遍历 doh_servers：每个 server 每级重试 1 次。
func queryLevelType(ctx context.Context, servers []string, name, qtype string, deadline time.Time) (ips []string, cname string, err error) {
	var lastErr error
	for _, srv := range servers {
		for attempt := 0; attempt < 2; attempt++ { // 每个 server 每级重试 1 次
			if time.Until(deadline) <= 0 {
				lastErr = fmt.Errorf("doh 全链路截止时间已到: %w", context.DeadlineExceeded)
				break
			}
			ips, cname, status, qerr := queryDOH(ctx, srv, name, qtype, deadline)
			if qerr != nil {
				lastErr = qerr
				continue // 同一 server 重试
			}
			if status == 3 {
				return nil, "", ErrNXDomain // 直接短路
			}
			if len(ips) == 0 && cname == "" {
				// NODATA：权威空答复，无需换 server；无数据返回让上层回退。
				return nil, "", nil
			}
			return ips, cname, nil
		}
	}
	return nil, "", lastErr
}

// queryDOH 向单个 DoH server 查询；返回命中记录（A/AAAA → ips、CNAME → cname）
// 与应答 Status。HTTP/解码错误返回非 nil error（调用方据此切换通道）。
func queryDOH(ctx context.Context, server, name, qtype string, deadline time.Time) (ips []string, cname string, status int, err error) {
	u, perr := url.Parse(server)
	if perr != nil {
		return nil, "", 0, fmt.Errorf("doh %s: %w", server, perr)
	}
	q := u.Query()
	q.Set("name", name)
	q.Set("type", qtype)
	u.RawQuery = q.Encode()

	req, rerr := http.NewRequestWithContext(ctx, http.MethodGet, u.String(), nil)
	if rerr != nil {
		return nil, "", 0, fmt.Errorf("doh %s: %w", server, rerr)
	}
	req.Header.Set("Accept", "application/dns-json")

	client := &http.Client{Timeout: attemptTimeout(deadline)}
	resp, rerr := client.Do(req)
	if rerr != nil {
		return nil, "", 0, fmt.Errorf("doh %s query %s %s: %w", server, name, qtype, rerr)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, "", 0, fmt.Errorf("doh %s query %s %s: http %d", server, name, qtype, resp.StatusCode)
	}
	// 响应体大小上限 1MB：防御恶意/异常超长应答打爆内存。
	resp.Body = http.MaxBytesReader(nil, resp.Body, maxDoHBodyBytes)
	var ans dohAnswer
	if derr := json.NewDecoder(resp.Body).Decode(&ans); derr != nil {
		return nil, "", 0, fmt.Errorf("doh %s query %s %s: 解码失败: %w", server, name, qtype, derr)
	}
	qname := strings.TrimSuffix(strings.ToLower(name), ".")
	wantA := qtype == "A"
	wantAAAA := qtype == "AAAA"
	for _, r := range ans.Answer {
		owner := strings.TrimSuffix(strings.ToLower(r.Name), ".")
		if owner != qname {
			continue
		}
		switch r.Type {
		case 1: // A（仅 A 查询收集，防御应答混入其它类型记录）
			if wantA {
				if ip := net.ParseIP(strings.TrimSpace(r.Data)); ip != nil && ip.To4() != nil {
					ips = append(ips, ip.To4().String())
				}
			}
		case 28: // AAAA（仅 AAAA 查询收集）
			if wantAAAA {
				if ip := net.ParseIP(strings.TrimSpace(r.Data)); ip != nil && ip.To16() != nil {
					ips = append(ips, strings.ToLower(ip.To16().String()))
				}
			}
		case 5: // CNAME（两种类型查询均可携带）
			if cname == "" {
				t := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(r.Data)), ".")
				if validName(t) {
					cname = t
				}
			}
		}
	}
	return ips, cname, ans.Status, nil
}

// ---------------------------------------------------------------------------
// 系统解析回退
// ---------------------------------------------------------------------------

// querySystem 经系统解析器（net.Resolver，PreferGo 语义跟随构建环境）解析
// name 到 IP；返回规范化 IP 列表与尽量取得的 canonical 名。NODATA/无解析结果
// 返回非 nil error；系统 NXDOMAIN 映射为 ErrNXDomain。
func querySystem(ctx context.Context, name string, qtypes []string, deadline time.Time) ([]string, string, error) {
	if time.Until(deadline) <= 0 {
		return nil, "", fmt.Errorf("system 解析无剩余时间: %w", context.DeadlineExceeded)
	}
	sctx, cancel := context.WithDeadline(ctx, deadline)
	defer cancel()
	r := &net.Resolver{}
	var ips []net.IP
	var err error
	switch {
	case len(qtypes) == 2:
		ips, err = r.LookupIP(sctx, "", name)
	case qtypes[0] == "AAAA":
		ips, err = r.LookupIP(sctx, "ip6", name)
	default:
		ips, err = r.LookupIP(sctx, "ip4", name)
	}
	if err != nil {
		var dnsErr *net.DNSError
		if errors.As(err, &dnsErr) && dnsErr.IsNotFound {
			return nil, "", fmt.Errorf("system 解析 %q: %w", name, ErrNXDomain)
		}
		return nil, "", err
	}
	if len(ips) == 0 {
		return nil, "", fmt.Errorf("system 解析 %q: 无结果", name)
	}
	out := make([]string, 0, len(ips))
	for _, ip := range ips {
		if v4 := ip.To4(); v4 != nil {
			out = append(out, v4.String())
		} else {
			out = append(out, strings.ToLower(ip.To16().String()))
		}
	}
	final := name
	if cn, cerr := r.LookupCNAME(sctx, name); cerr == nil {
		if t := strings.TrimSuffix(strings.ToLower(strings.TrimSpace(cn)), "."); t != "" && t != name {
			final = t
		}
	}
	return out, final, nil
}

// ---------------------------------------------------------------------------
// 判定与工具
// ---------------------------------------------------------------------------

const (
	modeDOH    = "doh"
	modeSystem = "system"

	maxCNAMEDepthDefault = 10 // DSD §1.1 默认值
	timeoutSecDefault    = 15 // DSD §1.1 默认值
	perAttemptTimeout    = 3 * time.Second
	maxDoHBodyBytes      = 1 << 20 // DoH 应答体大小上限 1MB
)

// attemptTimeout 单次 HTTP 尝试超时：约 3s，受全链路截止时间（timeout_sec）封顶。
func attemptTimeout(deadline time.Time) time.Duration {
	remain := time.Until(deadline)
	if remain <= 0 {
		return 0
	}
	if remain > perAttemptTimeout {
		return perAttemptTimeout
	}
	return remain
}

// qtypesFor 按 ip_version 映射查询类型（默认 ipv4）。
func qtypesFor(ipVersion string) []string {
	switch ipVersion {
	case "ipv6":
		return []string{"AAAA"}
	case "both":
		return []string{"A", "AAAA"}
	default:
		return []string{"A"}
	}
}

// sortIPs 按 DSD §5.3 排序：IPv4 字典序（To4().String()）在前，IPv6 字典序
// （To16().String()）在后；不可解析的串被丢弃。
func sortIPs(ips []string) []string {
	var v4, v6 []string
	for _, s := range ips {
		ip := net.ParseIP(strings.TrimSpace(s))
		if ip == nil {
			continue
		}
		if ip.To4() != nil {
			v4 = append(v4, ip.To4().String())
		} else {
			v6 = append(v6, strings.ToLower(ip.To16().String()))
		}
	}
	sort.Strings(v4)
	sort.Strings(v6)
	return append(v4, v6...)
}

// validName 判定合法 FQDN（与 DSD §1.2 一致）：多标签、标签含连字符但不起止
// 连字符、≤253 字符，不含尾点/下划线/空白。
func validName(s string) bool {
	if s == "" || len(s) > 253 {
		return false
	}
	if strings.HasSuffix(s, ".") || strings.ContainsAny(s, "_ \t") {
		return false
	}
	labels := strings.Split(s, ".")
	if len(labels) < 2 {
		return false
	}
	for _, l := range labels {
		if !validLabel(l) {
			return false
		}
	}
	return true
}

func validLabel(l string) bool {
	if l == "" || len(l) > 63 {
		return false
	}
	for i := 0; i < len(l); i++ {
		c := l[i]
		isAlphaNum := (c >= 'a' && c <= 'z') || (c >= '0' && c <= '9')
		if !isAlphaNum && c != '-' {
			return false
		}
		if c == '-' && (i == 0 || i == len(l)-1) {
			return false
		}
	}
	return true
}

// wrapResolveErr 将通道错误归一为 sentinel：已注释的 sentinel 原样透传，
// 其余（含超时、网络错误、时间耗尽）一律映射 ErrTimeout——DSD 语义：所有
// 解析通道失败 = timeout。
func wrapResolveErr(source string, err error, deadline time.Time) error {
	if err == nil {
		return fmt.Errorf("resolve %q: %w", source, ErrTimeout)
	}
	if errors.Is(err, ErrNXDomain) || errors.Is(err, ErrInvalidName) {
		return err
	}
	if isTimeoutErr(err) {
		return fmt.Errorf("resolve %q: %w (原因: %v)", source, ErrTimeout, err)
	}
	var dnsErr *net.DNSError
	if errors.As(err, &dnsErr) && (dnsErr.IsTimeout || dnsErr.IsTemporary) {
		return fmt.Errorf("resolve %q: %w (原因: %v)", source, ErrTimeout, err)
	}
	if !time.Now().Before(deadline) {
		return fmt.Errorf("resolve %q: %w (全链路截止时间到)", source, ErrTimeout)
	}
	return fmt.Errorf("resolve %q: %w (原因: %v)", source, ErrTimeout, err)
}

func isTimeoutErr(err error) bool {
	if errors.Is(err, context.DeadlineExceeded) {
		return true
	}
	var ne net.Error
	if errors.As(err, &ne) {
		return ne.Timeout()
	}
	return false
}

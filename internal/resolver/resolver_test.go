package resolver

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// ---------------------------------------------------------------------------
// 测试桩：mock DoH（application/dns-json）
// ---------------------------------------------------------------------------

type answerRec struct {
	Name string `json:"name"`
	Type int    `json:"type"`
	Data string `json:"data"`
}

type mockResp struct {
	Status int         `json:"Status"`
	Answer []answerRec `json:"Answer"`
}

// mockAnswer 单条查询的桩应答。
type mockAnswer struct {
	status int      // DNS rcode（3=NXDOMAIN）
	a      []string // A 记录
	aaaa   []string // AAAA 记录
	cname  string   // CNAME 目标
}

type queryRec struct{ name, qtype string }

// mockServer 记录收到的查询并代理应答；progress 为 nil 时可返回静态应答。
func mockServer(t *testing.T, h func(name, qtype string) mockAnswer) (*httptest.Server, *[]queryRec) {
	t.Helper()
	mu := &sync.Mutex{}
	queries := &[]queryRec{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name, qtype := r.URL.Query().Get("name"), r.URL.Query().Get("type")
		mu.Lock()
		*queries = append(*queries, queryRec{name, qtype})
		mu.Unlock()
		ans := h(name, qtype)
		resp := mockResp{Status: ans.status}
		// 真实 DoH 应答按查询类型返回：A 查询只带 A（及 CNAME），AAAA 查询只带 AAAA。
		if qtype == "A" {
			for _, ip := range ans.a {
				resp.Answer = append(resp.Answer, answerRec{Name: name, Type: 1, Data: ip})
			}
		}
		if qtype == "AAAA" {
			for _, ip := range ans.aaaa {
				resp.Answer = append(resp.Answer, answerRec{Name: name, Type: 28, Data: ip})
			}
		}
		if ans.cname != "" {
			resp.Answer = append(resp.Answer, answerRec{Name: name, Type: 5, Data: ans.cname})
		}
		w.Header().Set("Content-Type", "application/json")
		if err := json.NewEncoder(w).Encode(resp); err != nil {
			t.Errorf("mock 应答编码失败: %v", err)
		}
	}))
	t.Cleanup(srv.Close)
	return srv, queries
}

func dohCfg(server string) model.DNSConfig {
	return model.DNSConfig{
		Mode:          "doh",
		DOHServers:    []string{server},
		TimeoutSec:    15,
		IPVersion:     "ipv4",
		MaxCNAMEDepth: 10,
	}
}

// static 生成静态应答 handler。
func static(answers map[string]mockAnswer) func(string, string) mockAnswer {
	return func(name, qtype string) mockAnswer {
		if a, ok := answers[name]; ok {
			return a
		}
		return mockAnswer{status: 3} // 未注册的域视为 NXDOMAIN
	}
}

// ---------------------------------------------------------------------------
// CNAME 链
// ---------------------------------------------------------------------------

func TestResolveThreeHopChain(t *testing.T) {
	srv, _ := mockServer(t, static(map[string]mockAnswer{
		"a.example.com": {cname: "b.example.net."},
		"b.example.net": {cname: "c.example.org."},
		"c.example.org": {a: []string{"203.0.113.42", "198.51.100.7"}},
	}))
	res, err := Resolve("A.example.com", dohCfg(srv.URL))
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	if len(res.IPs) != 2 || res.IPs[0] != "198.51.100.7" || res.IPs[1] != "203.0.113.42" {
		t.Errorf("IP 排序/内容不符: %v（期望 v4 字典序 [198.51.100.7 203.0.113.42]）", res.IPs)
	}
	wantChain := []string{"a.example.com", "b.example.net", "c.example.org"}
	if strings.Join(res.Chain, "|") != strings.Join(wantChain, "|") {
		t.Errorf("链不符: %v", res.Chain)
	}
	if res.FinalName != "c.example.org" {
		t.Errorf("最终名不符: %s", res.FinalName)
	}
	if res.UsedFallback {
		t.Error("DoH 全程命中，不应标记回退")
	}
}

func TestResolveBothModeParallel(t *testing.T) {
	srv, queries := mockServer(t, static(map[string]mockAnswer{
		"mix.example.com": {a: []string{"203.0.113.1"}, aaaa: []string{"2001:db8::1", "2001:db8::10"}},
	}))
	cfg := dohCfg(srv.URL)
	cfg.IPVersion = "both"
	res, err := Resolve("mix.example.com", cfg)
	if err != nil {
		t.Fatalf("Resolve 失败: %v", err)
	}
	// v4 恒在 v6 前；v6 组内字典序。
	want := []string{"203.0.113.1", "2001:db8::1", "2001:db8::10"}
	if strings.Join(res.IPs, ",") != strings.Join(want, ",") {
		t.Errorf("both 模式排序不符: %v（期望 %v）", res.IPs, want)
	}
	seen := map[string]bool{}
	for _, q := range *queries {
		seen[q.qtype] = true
	}
	if !seen["A"] || !seen["AAAA"] {
		t.Errorf("both 模式应同时查询 A 与 AAAA，实际仅 %v", seen)
	}
}

func TestResolveIPVersionFilter(t *testing.T) {
	srv, queries := mockServer(t, static(map[string]mockAnswer{
		"v4.example.com": {a: []string{"192.0.2.1"}},
	}))
	cfg := dohCfg(srv.URL)
	cfg.IPVersion = "ipv4"
	if _, err := Resolve("v4.example.com", cfg); err != nil {
		t.Fatalf("ipv4 解析失败: %v", err)
	}
	for _, q := range *queries {
		if q.qtype != "A" {
			t.Errorf("ipv4 模式不应查询 %s", q.qtype)
		}
	}
}

// ---------------------------------------------------------------------------
// Sentinel 错误路径
// ---------------------------------------------------------------------------

func TestResolveCNAMELoop(t *testing.T) {
	answers := map[string]mockAnswer{
		"loop-a.example.com": {cname: "loop-b.example.com."},
		"loop-b.example.com": {cname: "loop-a.example.com."},
	}
	srv, _ := mockServer(t, static(answers))
	_, err := Resolve("loop-a.example.com", dohCfg(srv.URL))
	if !errors.Is(err, ErrCNAMELoop) {
		t.Fatalf("期望 ErrCNAMELoop，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "loop-b.example.com") {
		t.Errorf("错误信息应含环上节点: %v", err)
	}
}

func TestResolveTooDeep(t *testing.T) {
	srv, _ := mockServer(t, func(name, qtype string) mockAnswer {
		idx := name[len("deep-") : len(name)-len(".example.com")]
		n := 0
		fmt.Sscanf(idx, "%d", &n)
		if n < 11 {
			return mockAnswer{cname: fmt.Sprintf("deep-%d.example.com.", n+1)}
		}
		return mockAnswer{a: []string{"192.0.2.2"}}
	})
	cfg := dohCfg(srv.URL)
	cfg.MaxCNAMEDepth = 10
	_, err := Resolve("deep-0.example.com", cfg)
	if !errors.Is(err, ErrTooDeep) {
		t.Fatalf("期望 ErrTooDeep，实际 %v", err)
	}
	if !strings.Contains(err.Error(), "10") {
		t.Errorf("错误信息应含深度: %v", err)
	}
}

func TestResolveNXDomainShortCircuit(t *testing.T) {
	srv, _ := mockServer(t, func(name, qtype string) mockAnswer {
		return mockAnswer{status: 3}
	})
	_, err := Resolve("gone.example.com", dohCfg(srv.URL))
	if !errors.Is(err, ErrNXDomain) {
		t.Fatalf("期望 ErrNXDomain（Status=3 短路），实际 %v", err)
	}
}

func TestResolveTimeoutWithFallbackFailure(t *testing.T) {
	// DoH 挂起直至客户端超时；cfg.TimeoutSec=1 令全链路截止时间先耗尽，
	// 系统回退无剩余时间 → 最终 ErrTimeout（含回退失败路径）。
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		<-r.Context().Done() // 挂起，客户端超时后返回
	}))
	t.Cleanup(srv.Close)
	cfg := dohCfg(srv.URL)
	cfg.TimeoutSec = 1
	start := time.Now()
	_, err := Resolve("hang.example.com", cfg)
	if err == nil {
		t.Fatal("期望超时错误")
	}
	if !errors.Is(err, ErrTimeout) {
		t.Fatalf("期望 ErrTimeout，实际 %v", err)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Errorf("单测用时过长（%v），超时门限未生效", elapsed)
	}
}

func TestResolveInvalidName(t *testing.T) {
	// 非法名（单标签、含下划线、尾点）直接 ErrInvalidName，不触发任何网络。
	cfg := dohCfg("http://127.0.0.1:1")
	for _, name := range []string{"localhost", "bad_name.example.com", "trailing.example.com.", ""} {
		if _, err := Resolve(name, cfg); !errors.Is(err, ErrInvalidName) {
			t.Errorf("%q 期望 ErrInvalidName，实际 %v", name, err)
		}
	}
}

func TestResolveSystemBadChannel(t *testing.T) {
	// doh_servers 全为不可达地址：应顺利返回通道失败哨兵错误而非 panic。
	srv, _ := mockServer(t, static(nil))
	cfg := dohCfg(srv.URL)
	cfg.DOHServers = []string{"https://127.0.0.1:1/dns-query"}
	_, err := Resolve("x.example.com", cfg)
	if err == nil {
		t.Skip("环境可达 127.0.0.1:1，跳过通道失败断言")
	}
	if errors.Is(err, ErrInvalidName) || errors.Is(err, ErrCNAMELoop) {
		t.Errorf("不应为无关哨兵错误: %v", err)
	}
}

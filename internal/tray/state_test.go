package tray

import (
	"strings"
	"testing"
	"time"

	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// baseStatus 构造一个默认全绿的 status（UpdatedAt=now-1min，interval=5）。
func baseStatus(now time.Time) *model.SyncStatus {
	return &model.SyncStatus{
		Version:         model.StatusVersion,
		UpdatedAt:       now.Add(-time.Minute),
		IntervalMinutes: 5,
		Task:            model.TaskStatus{LastRunAt: now.Add(-time.Minute), LastRunOK: true},
		Entries: []model.EntryStatus{
			{ID: "e-01", Status: model.EntryOK, ResolvedIPs: []string{"203.0.113.1"}},
			{ID: "e-02", Status: model.EntryOK, ResolvedIPs: []string{"203.0.113.2"}},
		},
		HostsBlock: model.HostsBlockStatus{
			Present:     true,
			ContentMD5:  "a",
			ExpectedMD5: "a",
			LastWriteOK: true,
		},
	}
}

func TestStaleThreshold(t *testing.T) {
	cases := []struct {
		name string
		min  int
		want time.Duration
	}{
		{"interval=1 → 2min", 1, 2 * time.Minute},
		{"interval=5 → 10min", 5, 10 * time.Minute},
		{"interval=30 → 1h", 30, time.Hour},
		{"interval=200 → 6h40m 被 6h 封顶", 200, 6 * time.Hour},
		{"interval=1440 → 6h 封顶（DSD §7.10）", 1440, 6 * time.Hour},
		{"interval=0（异常）→ 夹取 2min", 0, 2 * time.Minute},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := StaleThreshold(c.min); got != c.want {
				t.Fatalf("StaleThreshold(%d) = %v, want %v", c.min, got, c.want)
			}
		})
	}
}

func TestStale(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	cases := []struct {
		name string
		s    *model.SyncStatus
		age  time.Duration // UpdatedAt = now-age
		want bool
	}{
		{"nil → 陈旧", nil, 0, true},
		{"零值时间戳 → 陈旧", &model.SyncStatus{UpdatedAt: time.Time{}}, 0, true},
		{"新鲜（1min < 10min）", baseStatus(now), time.Minute, false},
		{"恰好阈值边界 → 不陈旧（严格大于）", baseStatus(now), 10 * time.Minute, false},
		{"超阈值 → 陈旧", baseStatus(now), 10*time.Minute + time.Second, true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			st := c.s
			if st != nil && !st.UpdatedAt.IsZero() && c.age > 0 {
				st.UpdatedAt = now.Add(-c.age)
			}
			if got := Stale(st, now); got != c.want {
				t.Fatalf("Stale() = %v, want %v", got, c.want)
			}
		})
	}
}

func TestDecideColorGray(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	s := baseStatus(now)
	s.UpdatedAt = now.AddDate(0, 0, -2) // 陈旧 2 天
	if got := DecideColor(s, now); got != ColorGray {
		t.Fatalf("陈旧 status 应为灰，得 %v", got)
	}
	// 陈旧优先于红：即使有严重故障也先报灰（DSD §4.3：灰独立通道优先判定）。
	bad := baseStatus(now)
	bad.UpdatedAt = now.Add(-7 * time.Hour)
	bad.HostsBlock.LastWriteOK = false
	if got := DecideColor(bad, now); got != ColorGray {
		t.Fatalf("陈旧且故障时应为灰（陈旧通道优先），得 %v", got)
	}
	// interval=1440 时 5h 未更新仍不灰（6h 封顶）。
	long := baseStatus(now)
	long.IntervalMinutes = 1440
	long.UpdatedAt = now.Add(-5 * time.Hour)
	if got := DecideColor(long, now); got != ColorGreen {
		t.Fatalf("interval=1440 且 5h 未更新应非灰（封顶 6h），得 %v", got)
	}
}

func TestDecideColorRed(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

	t.Run("red1: last_run_ok=false 且含写盘错误", func(t *testing.T) {
		s := baseStatus(now)
		s.Task.LastRunOK = false
		s.Task.LastError = "hosts 写盘失败，旧 hosts 保留原样: disk full"
		if got := DecideColor(s, now); got != ColorRed {
			t.Fatalf("写盘失败应红，得 %v", got)
		}
	})

	t.Run("red1 反例: run_ok=false 但错误非写盘（如 timeout）→ 不红", func(t *testing.T) {
		s := baseStatus(now)
		s.Task.LastRunOK = false
		s.Task.LastError = "全部 DoH 与系统解析超时"
		if got := DecideColor(s, now); got == ColorRed {
			t.Fatal("非写盘失败不应红")
		}
	})

	t.Run("red2: hosts_block.last_write_ok=false", func(t *testing.T) {
		s := baseStatus(now)
		s.HostsBlock.LastWriteOK = false
		if got := DecideColor(s, now); got != ColorRed {
			t.Fatalf("hosts 块写失败应红，得 %v", got)
		}
	})

	t.Run("red3: 块缺失且有条目", func(t *testing.T) {
		s := baseStatus(now)
		s.HostsBlock.Present = false
		if got := DecideColor(s, now); got != ColorRed {
			t.Fatalf("块缺失且有条目应红，得 %v", got)
		}
	})

	t.Run("red3 反例: 块缺失但无条目 → 不红", func(t *testing.T) {
		s := baseStatus(now)
		s.HostsBlock.Present = false
		s.Entries = nil
		if got := DecideColor(s, now); got == ColorRed {
			t.Fatal("无条目时块缺失不应红")
		}
	})

	t.Run("red4: 条目 write_failed", func(t *testing.T) {
		s := baseStatus(now)
		s.Entries[0].Status = model.EntryWriteFailed
		s.Task.LastError = ""
		if got := DecideColor(s, now); got != ColorRed {
			t.Fatalf("条目 write_failed 应红（§3.2），得 %v", got)
		}
	})

	t.Run("优先级: 红覆盖黄", func(t *testing.T) {
		s := baseStatus(now)
		s.Entries[0].Status = model.EntryNXDomain // 黄条件
		s.HostsBlock.LastWriteOK = false          // 红条件
		if got := DecideColor(s, now); got != ColorRed {
			t.Fatalf("红黄并存应红，得 %v", got)
		}
	})
}

func TestDecideColorYellow(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

	for _, es := range []model.EntryState{
		model.EntryNXDomain, model.EntryCNAMELoop, model.EntryTooDeep,
		model.EntryTimeout, model.EntryInvalidConfig,
	} {
		t.Run("条目状态="+string(es), func(t *testing.T) {
			s := baseStatus(now)
			s.Entries[1].Status = es
			if got := DecideColor(s, now); got != ColorYellow {
				t.Fatalf("条目 %s 应黄，得 %v", es, got)
			}
		})
	}

	t.Run("warn: 前缀（备份失败）", func(t *testing.T) {
		s := baseStatus(now)
		s.Task.LastError = "warn: backup 失败: permission denied"
		if got := DecideColor(s, now); got != ColorYellow {
			t.Fatalf("warn: 前缀应黄，得 %v", got)
		}
	})

	t.Run("篡改自愈警告", func(t *testing.T) {
		s := baseStatus(now)
		s.Task.LastError = "检测到块缺失/篡改，已自动重写"
		if got := DecideColor(s, now); got != ColorYellow {
			t.Fatalf("篡改自愈应黄（§3.2 本轮有 warn），得 %v", got)
		}
	})

	t.Run("paused 不引起黄", func(t *testing.T) {
		s := baseStatus(now)
		s.Entries[0].Status = model.EntryPaused
		s.Entries[1].Status = model.EntryPaused
		if got := DecideColor(s, now); got != ColorGreen {
			t.Fatalf("全 paused 应绿，得 %v", got)
		}
	})
}

func TestDecideColorGreen(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)
	s := baseStatus(now)
	if got := DecideColor(s, now); got != ColorGreen {
		t.Fatalf("全 ok 应绿，得 %v", got)
	}
	// used_fallback 是成功路径注解，不改变绿标（DSD §1.5）。
	s.Entries[0].UsedFallback = true
	s.Entries[0].Error = "DoH 不可用已回退系统解析"
	if got := DecideColor(s, now); got != ColorGreen {
		t.Fatalf("回退成功仍应绿，得 %v", got)
	}
}

func TestColorIconNameAndLabel(t *testing.T) {
	table := []struct {
		c    Color
		icon string
	}{
		{ColorGray, "gray"},
		{ColorGreen, "green"},
		{ColorYellow, "yellow"},
		{ColorRed, "red"},
	}
	for _, tc := range table {
		if got := tc.c.IconName(); got != tc.icon {
			t.Fatalf("%v.IconName() = %q, want %q", tc.c, got, tc.icon)
		}
		if got := tc.c.Label(); got == "" {
			t.Fatalf("%v.Label() 不得为空", tc.c)
		}
	}
}

func TestFirstAlert(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

	t.Run("无告警", func(t *testing.T) {
		s := baseStatus(now)
		if _, ok := FirstAlert(s); ok {
			t.Fatal("全绿不应有告警")
		}
	})

	t.Run("nil 无告警", func(t *testing.T) {
		if _, ok := FirstAlert(nil); ok {
			t.Fatal("nil 不应有告警")
		}
	})

	t.Run("task.last_error 优先", func(t *testing.T) {
		s := baseStatus(now)
		s.Task.LastError = "hosts 写盘失败，旧 hosts 保留原样: disk full"
		msg, ok := FirstAlert(s)
		if !ok || !strings.Contains(msg, "写盘失败") {
			t.Fatalf("应返回 last_error 告警，得 %q ok=%v", msg, ok)
		}
	})

	t.Run("warn: 前缀剥除", func(t *testing.T) {
		s := baseStatus(now)
		s.Task.LastError = "warn: backup 失败: disk"
		msg, ok := FirstAlert(s)
		if !ok || strings.HasPrefix(msg, "warn:") {
			t.Fatalf("warn: 前缀应剥除，得 %q", msg)
		}
	})

	t.Run("取首个非 ok/paused 条目错误", func(t *testing.T) {
		s := baseStatus(now)
		s.Entries[0].Status = model.EntryTimeout
		s.Entries[0].Error = "解析超时（15s），保留上一次条目"
		s.Entries[1].Error = "第二条错误"
		msg, ok := FirstAlert(s)
		if !ok || !strings.Contains(msg, "解析超时") {
			t.Fatalf("应取首条条目错误，得 %q", msg)
		}
	})

	t.Run("paused 条目错误不取", func(t *testing.T) {
		s := baseStatus(now)
		s.Entries[0].Status = model.EntryPaused
		s.Entries[0].Error = "不展示的错误"
		if msg, ok := FirstAlert(s); ok && strings.Contains(msg, "不展示") {
			t.Fatalf("paused 条目错误不应成为告警，得 %q", msg)
		}
	})

	t.Run("task 错误优先于条目错误", func(t *testing.T) {
		s := baseStatus(now)
		s.Task.LastError = "warn: flushdns 失败: 权限不足"
		s.Entries[0].Error = "条目错误"
		msg, _ := FirstAlert(s)
		if !strings.Contains(msg, "flushdns") {
			t.Fatalf("task.last_error 应优先，得 %q", msg)
		}
	})
}

func TestTooltip(t *testing.T) {
	now := time.Date(2026, 9, 24, 10, 0, 0, 0, time.UTC)

	t.Run("nil → 灰文案", func(t *testing.T) {
		if got := Tooltip(nil, ColorGray); got != "后台同步未运行，请检查计划任务" {
			t.Fatalf("nil 应为灰文案，得 %q", got)
		}
	})

	t.Run("灰色 → 灰文案", func(t *testing.T) {
		if got := Tooltip(baseStatus(now), ColorGray); !strings.Contains(got, "未运行") {
			t.Fatalf("灰应提示未运行，得 %q", got)
		}
	})

	t.Run("绿无告警 → 仅最近同步时间", func(t *testing.T) {
		got := Tooltip(baseStatus(now), ColorGreen)
		// 时区不在此断言：本机为 UTC+8，只匹配日期与时间格式前缀。
		if !strings.Contains(got, "最近同步 2026-09-24 ") || !strings.Contains(got, ":59:00") {
			t.Fatalf("应含最近同步时间，得 %q", got)
		}
		if strings.Contains(got, "告警") {
			t.Fatalf("全绿不应含告警，得 %q", got)
		}
	})

	t.Run("黄有告警 → 时间+告警", func(t *testing.T) {
		s := baseStatus(now)
		s.Entries[0].Status = model.EntryNXDomain
		s.Entries[0].Error = "解析链上不存在域名 relay.example.net"
		got := Tooltip(s, ColorYellow)
		if !strings.Contains(got, "最近同步") || !strings.Contains(got, "告警: 解析链上不存在域名") {
			t.Fatalf("应含时间与告警，得 %q", got)
		}
	})
}

package platform

import (
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// B1：icacls 逐参断言 + B2 权限矩阵一致性（纯函数，任意平台可测）
// ---------------------------------------------------------------------------

// TestIcaaclsRulesArgvShape B1 核心回归：每条规则的 args 必须为逐段
// 参数（常量数组），任一元素不得含 ASCII 空格——整串 ACL 作为单个 argv
// 会被 exec.Command 整体加引号触发 icacls "Invalid parameter(s)"。
func TestIcaaclsRulesArgvShape(t *testing.T) {
	rules := windowsACLRules(`C:\ProgramData\ddns-hosts-sync`)
	if len(rules) != 4 {
		t.Fatalf("规则数 = %d, want 4（根/config/state/logs）", len(rules))
	}
	wantDirs := []string{
		`C:\ProgramData\ddns-hosts-sync`,
		`C:\ProgramData\ddns-hosts-sync\config`,
		`C:\ProgramData\ddns-hosts-sync\state`,
		`C:\ProgramData\ddns-hosts-sync\logs`,
	}
	for i, r := range rules {
		if r.dir != wantDirs[i] {
			t.Errorf("规则[%d] dir = %q, want %q", i, r.dir, wantDirs[i])
		}
		if len(r.args) == 0 {
			t.Fatalf("规则[%d] args 不得为空", i)
		}
		for j, a := range r.args {
			if strings.ContainsAny(a, " \t") {
				t.Errorf("规则[%d] args[%d] 含空白（不得整串传入）: %q", i, j, a)
			}
		}
	}
}

// TestIcaaclsRootRuleExactParams 根目录规则逐参精确断言（防顺序漂移）。
func TestIcaaclsRootRuleExactParams(t *testing.T) {
	rules := windowsACLRules(`C:\ProgData`)
	r := rules[0]
	want := []string{
		"/inheritance:r",
		"/grant:r", "*S-1-5-18:(OI)(CI)F",
		"/grant:r", "*S-1-5-32-544:(OI)(CI)F",
		"/grant:r", "*S-1-5-32-545:(OI)(CI)R",
		"/T",
	}
	if strings.Join(r.args, " ") != strings.Join(want, " ") {
		t.Errorf("根规则逐参 = %v, want %v", r.args, want)
	}
}

// TestIcaaclsPermissionMatrix B2 权限矩阵：state=R / config trigger=M。
//   - config\：Users 含 M（trigger.json 落此目录可写）；
//   - state\ / logs\：Users 仅 R，绝不含 M（GUI 只读防误写状态）；
//   - 根：Users 仅 R（列目录）。
func TestIcaaclsPermissionMatrix(t *testing.T) {
	rules := windowsACLRules(`C:\ProgramData\ddns-hosts-sync`)
	usersGrant := func(args []string) string {
		for _, a := range args {
			if strings.Contains(a, sidUsers) {
				return a
			}
		}
		return ""
	}
	configRule, stateRule, logsRule := rules[1], rules[2], rules[3]

	if g := usersGrant(configRule.args); !strings.Contains(g, sidUsers+":(OI)(CI)M") {
		t.Errorf("config\\ 应给 Users M（trigger 可写），实际 %q", g)
	}
	if g := usersGrant(stateRule.args); !strings.Contains(g, sidUsers+":(OI)(CI)R") {
		t.Errorf("state\\ 应给 Users R，实际 %q", g)
	}
	if g := usersGrant(logsRule.args); !strings.Contains(g, sidUsers+":(OI)(CI)R") {
		t.Errorf("logs\\ 应给 Users R，实际 %q", g)
	}
	for _, r := range []icaclsRule{stateRule, logsRule} {
		for _, a := range r.args {
			if strings.Contains(a, sidUsers+":(OI)(CI)M") {
				t.Errorf("%s 含 Users M（应为只读收口）: %q", r.dir, a)
			}
		}
	}
}

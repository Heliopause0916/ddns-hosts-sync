package tasks

import (
	"encoding/xml"
	"strings"
	"testing"
)

// ---------------------------------------------------------------------------
// 测试用镜像结构（独立于生成结构，防止"用生成器结构验证生成器"假阳性）
// ---------------------------------------------------------------------------

type tTask struct {
	XMLName  xml.Name `xml:"Task"`
	Version  string   `xml:"version,attr"`
	Triggers struct {
		BootTrigger []struct {
			Enabled string `xml:"Enabled"`
		} `xml:"BootTrigger"`
		TimeTrigger []struct {
			StartBoundary string `xml:"StartBoundary"`
			Repetition    struct {
				Interval          string `xml:"Interval"`
				StopAtDurationEnd string `xml:"StopAtDurationEnd"`
			} `xml:"Repetition"`
			Enabled string `xml:"Enabled"`
		} `xml:"TimeTrigger"`
	} `xml:"Triggers"`
	Principals struct {
		Principal []struct {
			ID       string `xml:"Id,attr"`
			UserID   string `xml:"UserId"`
			RunLevel string `xml:"RunLevel"`
		} `xml:"Principal"`
	} `xml:"Principals"`
	Settings struct {
		MultipleInstancesPolicy    string `xml:"MultipleInstancesPolicy"`
		ExecutionTimeLimit         string `xml:"ExecutionTimeLimit"`
		DisallowStartIfOnBatteries string `xml:"DisallowStartIfOnBatteries"`
	} `xml:"Settings"`
	Actions struct {
		Exec []struct {
			Command   string `xml:"Command"`
			Arguments string `xml:"Arguments"`
		} `xml:"Exec"`
	} `xml:"Actions"`
}

func mustTaskXML(t *testing.T, execPath string, args ...string) tTask {
	t.Helper()
	doc, err := BuildTaskXML(TaskSpec{Name: "ddns-hosts-sync", ExecPath: execPath, Args: args})
	if err != nil {
		t.Fatalf("BuildTaskXML: %v", err)
	}
	var got tTask
	if err := xml.Unmarshal(doc, &got); err != nil {
		t.Fatalf("反序列化生成的 XML 失败: %v\n%s", err, doc)
	}
	return got
}

// TestBuildTaskXML_FullStructure DSD §5.2 全结构断言：Task version=1.2、
// BootTrigger+TimeTrigger（StartBoundary/PT1M/StopAtDurationEnd=false）、
// SID S-1-5-18、RunLevel HighestAvailable、Settings 三项、Exec 独立字段。
func TestBuildTaskXML_FullStructure(t *testing.T) {
	execPath := `C:\Program Files\ddns-hosts-sync\ddns-hosts-sync.exe`
	got := mustTaskXML(t, execPath, "sync")

	if got.Version != "1.2" {
		t.Errorf("Task version 应为 1.2，实际 %q", got.Version)
	}
	if len(got.Triggers.BootTrigger) != 1 {
		t.Fatalf("应恰有 1 个 BootTrigger，实际 %d", len(got.Triggers.BootTrigger))
	}
	if e := got.Triggers.BootTrigger[0].Enabled; e != "true" {
		t.Errorf("BootTrigger Enabled 应为 true，实际 %q", e)
	}
	if len(got.Triggers.TimeTrigger) != 1 {
		t.Fatalf("应恰有 1 个 TimeTrigger，实际 %d", len(got.Triggers.TimeTrigger))
	}
	tt := got.Triggers.TimeTrigger[0]
	if tt.StartBoundary != "2026-01-01T00:00:00" {
		t.Errorf("TimeTrigger StartBoundary 应为 2026-01-01T00:00:00，实际 %q", tt.StartBoundary)
	}
	if tt.Repetition.Interval != "PT1M" {
		t.Errorf("Repetition Interval 应为 PT1M，实际 %q", tt.Repetition.Interval)
	}
	if tt.Repetition.StopAtDurationEnd != "false" {
		t.Errorf("StopAtDurationEnd 应为 false，实际 %q", tt.Repetition.StopAtDurationEnd)
	}
	if tt.Enabled != "true" {
		t.Errorf("TimeTrigger Enabled 应为 true，实际 %q", tt.Enabled)
	}
	if len(got.Principals.Principal) != 1 {
		t.Fatalf("应恰有 1 个 Principal，实际 %d", len(got.Principals.Principal))
	}
	pr := got.Principals.Principal[0]
	if pr.UserID != "S-1-5-18" {
		t.Errorf("Principal UserId 应为 S-1-5-18（SYSTEM），实际 %q", pr.UserID)
	}
	if pr.RunLevel != "HighestAvailable" {
		t.Errorf("Principal RunLevel 应为 HighestAvailable，实际 %q", pr.RunLevel)
	}
	if pr.ID != "Author" {
		t.Errorf("Principal Id 应为 Author，实际 %q", pr.ID)
	}
	if got.Settings.MultipleInstancesPolicy != "IgnoreNew" {
		t.Errorf("MultipleInstancesPolicy 应为 IgnoreNew，实际 %q", got.Settings.MultipleInstancesPolicy)
	}
	if got.Settings.ExecutionTimeLimit != "PT10M" {
		t.Errorf("ExecutionTimeLimit 应为 PT10M，实际 %q", got.Settings.ExecutionTimeLimit)
	}
	if got.Settings.DisallowStartIfOnBatteries != "false" {
		t.Errorf("DisallowStartIfOnBatteries 应为 false，实际 %q", got.Settings.DisallowStartIfOnBatteries)
	}
	if len(got.Actions.Exec) != 1 {
		t.Fatalf("应恰有 1 个 Exec 动作，实际 %d", len(got.Actions.Exec))
	}
	ex := got.Actions.Exec[0]
	if ex.Command != execPath {
		t.Errorf("Exec Command 应等于 ExecPath，实际 %q", ex.Command)
	}
	if ex.Arguments != "sync" {
		t.Errorf("Exec Arguments 应为 sync，实际 %q", ex.Arguments)
	}
}

// TestBuildTaskXML_EscapesXMLSpecialChars ExecPath 含 XML 元字符 &<> 时
// 必须被转义（路径经 encoding/xml 序列化，绝不能破坏 XML 结构）。
func TestBuildTaskXML_EscapesXMLSpecialChars(t *testing.T) {
	path := `C:\Prog&ram<x>Files\ddns.exe`
	got := mustTaskXML(t, path, "sync")
	if got.Actions.Exec[0].Command != path {
		t.Errorf("反序列化后 Command 应还原为原始路径 %q，实际 %q", path, got.Actions.Exec[0].Command)
	}
	doc, _ := BuildTaskXML(TaskSpec{Name: "n", ExecPath: path, Args: []string{"sync"}})
	text := string(doc)
	for _, esc := range []string{"&amp;", "&lt;", "&gt;"} {
		if !strings.Contains(text, esc) {
			t.Errorf("原始 XML 应含转义序列 %q:\n%s", esc, text)
		}
	}
	if strings.Contains(text, "ram<x>") {
		t.Errorf("原始 XML 不得含未转义元字符:\n%s", text)
	}
}

// TestBuildTaskXML_ArgsEmpty 无参数时 Arguments 为空字符串（字段独立存在）。
func TestBuildTaskXML_ArgsEmpty(t *testing.T) {
	got := mustTaskXML(t, "/opt/ddns-hosts-sync")
	if got.Actions.Exec[0].Arguments != "" {
		t.Errorf("空 Args 时 Arguments 应为空，实际 %q", got.Actions.Exec[0].Arguments)
	}
}

// taskSchemaNamespace Task Scheduler schema 命名空间：schtasks 报错
// "(2,3):Task:" 的根因即 MSXML 要求根元素 <Task> 属于该命名空间（冒烟 Bug 1），
// 缺少时根元素 schema 校验失败、install/Register 直接报错。
const taskSchemaNamespace = "http://schemas.microsoft.com/windows/2004/02/mit/task"

// TestBuildTaskXML_TaskNamespace 根元素必须携带 Task Scheduler schema 命名空间：
//
//  1. Marshal 原始输出包含带 xmlns 声明与 version="1.2" 的根元素首行；
//  2. 输出可反序列化回带 Space 的 xml.Name（"space local" 形式）且 Space
//     与命名空间一致。
func TestBuildTaskXML_TaskNamespace(t *testing.T) {
	doc, err := BuildTaskXML(TaskSpec{
		Name:     "ddns-hosts-sync",
		ExecPath: `C:\Program Files\ddns-hosts-sync\ddns-hosts-sync.exe`,
		Args:     []string{"sync"},
	})
	if err != nil {
		t.Fatalf("BuildTaskXML: %v", err)
	}
	root := `<Task xmlns="` + taskSchemaNamespace + `" version="1.2">`
	if text := string(doc); !strings.Contains(text, root) {
		t.Errorf("根元素应输出 %q:\n%s", root, text)
	}
	var got struct {
		XMLName xml.Name `xml:"http://schemas.microsoft.com/windows/2004/02/mit/task Task"`
	}
	if err := xml.Unmarshal(doc, &got); err != nil {
		t.Fatalf("反序列化生成的 XML 失败: %v\n%s", err, doc)
	}
	if got.XMLName.Space != taskSchemaNamespace {
		t.Errorf("反序列化后 XMLName.Space 应为 %q，实际 %q", taskSchemaNamespace, got.XMLName.Space)
	}
}

// TestBuildTaskXML_RepetitionOmitDuration Repetition 必须省略 <Duration>：
// schtasks/MSXML 真机校验对 <Duration>PT0S</Duration>（零时长）拒收报
// "(14,26):Duration:PT0S"；schema 中 duration 可选（minOccurs=0），省略即
// 无限重复，语义与 PT0S 等价。断言输出 XML 不含该元素、也不含 PT0S 常量；
// Repetition 其余字段（Interval/StopAtDurationEnd）仍在（FullStructure 覆盖）。
func TestBuildTaskXML_RepetitionOmitDuration(t *testing.T) {
	doc, err := BuildTaskXML(TaskSpec{
		Name:     "ddns-hosts-sync",
		ExecPath: `C:\Program Files\ddns-hosts-sync\ddns-hosts-sync.exe`,
		Args:     []string{"sync"},
	})
	if err != nil {
		t.Fatalf("BuildTaskXML: %v", err)
	}
	text := string(doc)
	if strings.Contains(text, "<Duration>") {
		t.Errorf("输出 XML 不得含 <Duration> 元素（PT0S 被 schtasks 拒收，省略即无限重复）:\n%s", text)
	}
	if strings.Contains(text, "PT0S") {
		t.Errorf("输出 XML 不得含 PT0S 常量:\n%s", text)
	}
}

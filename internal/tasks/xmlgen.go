package tasks

import "encoding/xml"

// ---------------------------------------------------------------------------
// schtasks XML 生成（DSD §5.2）
//
// 本文件为无 build tag 的纯生成逻辑，任意平台可编译可单测（DSD §7.3：
// XML 用 encoding/xml 生成，禁止手工字符串拼接）。返回 UTF-8 字节的
// <Task> 文档（不含 XML 声明）；schtasks 要求的 UTF-16 编码与 BOM 由
// Windows 侧写入临时文件时完成（见 tasks_windows.go）。
// ---------------------------------------------------------------------------

// xRegistrationInfo <RegistrationInfo> 描述信息（展示用）。
type xRegistrationInfo struct {
	Description string `xml:"Description"`
}

// xBootTrigger 开机触发器：Enabled 恒 true。
type xBootTrigger struct {
	Enabled string `xml:"Enabled"`
}

// xRepetition <Repetition>：PT1M 重复、PT0S 无限期、StopAtDurationEnd=false。
type xRepetition struct {
	Interval          string `xml:"Interval"`
	Duration          string `xml:"Duration"`
	StopAtDurationEnd string `xml:"StopAtDurationEnd"`
}

// xTimeTrigger 时间触发器：StartBoundary 固定 2026-01-01T00:00:00（DSD §5.2），
// 每分钟重复由 Repetition 承担，NeverStop 语义由 PT0S 实现。
type xTimeTrigger struct {
	StartBoundary string      `xml:"StartBoundary"`
	Repetition    xRepetition `xml:"Repetition"`
	Enabled       string      `xml:"Enabled"`
}

// xTriggers 两触发器按 DSD §5.2 顺序：BootTrigger 在前、TimeTrigger 在后。
type xTriggers struct {
	BootTrigger xBootTrigger `xml:"BootTrigger"`
	TimeTrigger xTimeTrigger `xml:"TimeTrigger"`
}

// xPrincipal 主体：UserId=S-1-5-18（SYSTEM）、RunLevel=HighestAvailable。
type xPrincipal struct {
	ID       string `xml:"Id,attr"`
	UserID   string `xml:"UserId"`
	RunLevel string `xml:"RunLevel"`
}

type xPrincipals struct {
	Principal xPrincipal `xml:"Principal"`
}

// xSettings 任务设置：IgnoreNew（串行，防堆积）、PT10M 执行上限、电池不过滤。
type xSettings struct {
	MultipleInstancesPolicy    string `xml:"MultipleInstancesPolicy"`
	ExecutionTimeLimit         string `xml:"ExecutionTimeLimit"`
	DisallowStartIfOnBatteries string `xml:"DisallowStartIfOnBatteries"`
}

// xExec <Exec> 执行动作：Command 承接 ExecPath（含空格路径，引号交由 Task
// Scheduler 处理，DSD §7.3/§5.2），Arguments 独立字段，不含任何相对路径。
type xExec struct {
	Command   string `xml:"Command"`
	Arguments string `xml:"Arguments"`
}

type xActions struct {
	Exec xExec `xml:"Exec"`
}

// xTask 顶层 <Task version="1.2">，字段顺序即 XML 输出顺序（DSD §5.2）。
type xTask struct {
	XMLName          xml.Name          `xml:"Task"`
	Version          string            `xml:"version,attr"`
	RegistrationInfo xRegistrationInfo `xml:"RegistrationInfo"`
	Triggers         xTriggers         `xml:"Triggers"`
	Principals       xPrincipals       `xml:"Principals"`
	Settings         xSettings         `xml:"Settings"`
	Actions          xActions          `xml:"Actions"`
}

// BuildTaskXML 由 spec 生成 schtasks 注册用的 <Task> XML 文档（UTF-8 字节，
// 无 XML 声明；BootTrigger+TimeTrigger、SYSTEM SID、PT1M/PT0S 等结构常量
// 按 DSD §5.2 冻结）。生成失败返回 error（编码/xml 序列化异常，理论不可达）。
func BuildTaskXML(spec TaskSpec) ([]byte, error) {
	doc := xTask{
		Version: "1.2",
		RegistrationInfo: xRegistrationInfo{
			Description: "ddns-hosts-sync background sync",
		},
		Triggers: xTriggers{
			BootTrigger: xBootTrigger{Enabled: "true"},
			TimeTrigger: xTimeTrigger{
				StartBoundary: "2026-01-01T00:00:00",
				Repetition: xRepetition{
					Interval:          "PT1M",
					Duration:          "PT0S",
					StopAtDurationEnd: "false",
				},
				Enabled: "true",
			},
		},
		Principals: xPrincipals{
			Principal: xPrincipal{
				ID:       "Author",
				UserID:   "S-1-5-18", // SYSTEM（DSD §7.3：写死 SID，不用 /RU 名称）
				RunLevel: "HighestAvailable",
			},
		},
		Settings: xSettings{
			MultipleInstancesPolicy:    "IgnoreNew",
			ExecutionTimeLimit:         "PT10M",
			DisallowStartIfOnBatteries: "false",
		},
		Actions: xActions{
			Exec: xExec{
				Command:   spec.ExecPath,
				Arguments: joinArgs(spec.Args),
			},
		},
	}
	return xml.MarshalIndent(doc, "", "  ")
}

// joinArgs 将 []string 拼为单行 Arguments（空格分隔；各参数不含 shell 元字符，
// 任务注册由 Task Scheduler 直启，不经 shell，无注入面）。
func joinArgs(args []string) string {
	out := ""
	for i, a := range args {
		if i > 0 {
			out += " "
		}
		out += a
	}
	return out
}

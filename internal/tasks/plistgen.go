package tasks

import (
	"bytes"
	"strings"
	"text/template"
)

// ---------------------------------------------------------------------------
// launchd plist 生成（macOS，ARCHITECTURE §5.2）
//
// 纯生成逻辑，任意平台可编译可单测。模板固定：Label=com.<name>、StartInterval
// =60（与 Windows 任务同构的每分钟粒度）、RunAtLoad=true。ProgramArguments 为
// [ExecPath, "sync"]。
// ---------------------------------------------------------------------------

// plistTemplate 固定模板。launchd plist 是 Apple DTD 的 XML；用 text/template
// 生成（非 schtasks XML 那种数据密集结构，无需 encoding/xml 对象模型），
// 用户可控字节（ExecPath）经 plistEscape 转义，防止路径含 &<> 破坏 XML。
var plistTemplate = template.Must(template.New("launchd-plist").
	Funcs(template.FuncMap{"plistEscape": plistEscape}).
	Parse(`<?xml version="1.0" encoding="UTF-8"?>
<!DOCTYPE plist PUBLIC "-//Apple//DTD PLIST 1.0//EN" "http://www.apple.com/DTDs/PropertyList-1.0.dtd">
<plist version="1.0">
<dict>
	<key>Label</key>
	<string>{{.Label}}</string>
	<key>ProgramArguments</key>
	<array>{{range .ProgramArguments}}
		<string>{{. | plistEscape}}</string>{{end}}
	</array>
	<key>RunAtLoad</key>
	<true/>
	<key>StartInterval</key>
	<integer>60</integer>
</dict>
</plist>
`))

// launchLabel 由注册名派生 launchd Label：com.<name>（如 com.ddns-hosts-sync）。
// 文件路径约定 /Library/LaunchDaemons/com.ddns-hosts-sync.plist（§5.2）。
func launchLabel(name string) string {
	return "com." + name
}

// BuildLaunchdPlist 生成 LaunchDaemon plist 字节（UTF-8，含 XML 声明）。
func BuildLaunchdPlist(spec TaskSpec) ([]byte, error) {
	progArgs := make([]string, 0, len(spec.Args)+1)
	progArgs = append(progArgs, spec.ExecPath)
	progArgs = append(progArgs, spec.Args...)

	funcs := template.FuncMap{"plistEscape": plistEscape}
	data := struct {
		Label            string
		ProgramArguments []string
	}{Label: launchLabel(spec.Name), ProgramArguments: progArgs}

	var buf bytes.Buffer
	t := template.Must(plistTemplate.Clone()).Funcs(funcs)
	if err := t.Execute(&buf, data); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// plistEscape 转义 XML 元素文本中的 & < >（ExecPath 唯一外部可控字节）。
func plistEscape(s string) string {
	r := strings.NewReplacer("&", "&amp;", "<", "&lt;", ">", "&gt;")
	return r.Replace(s)
}

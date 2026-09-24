package gui

import (
	"fmt"
	"strings"

	"github.com/heliopause/ddns-hosts-sync/internal/config"
	"github.com/heliopause/ddns-hosts-sync/internal/model"
)

// noteMaxRunes 备注长度软限制（DSD §1.1 规则 10：≤200 字符）。
const noteMaxRunes = 200

// ---------------------------------------------------------------------------
// GUI 端校验（DSD §1.7 规则表）：失焦即时校验与提交权威校验共用判定逻辑，
// 权威判定仍然复用 internal/config（判定逻辑单源），此处补充 target 全局
// 去重（规则 4，需要条目集合上下文）与可读性包装。
// ---------------------------------------------------------------------------

// validateEntryInput 单条目即时校验（规则 2/3 + 备注软限制）。
// 返回可读的校验错误；empty=true 表示还可用于新增对话框的空值提示。
func validateEntryInput(e model.Entry) error {
	if err := config.ValidateEntry(e); err != nil {
		return err
	}
	if rn := []rune(e.Note); len(rn) > noteMaxRunes {
		return fmt.Errorf("备注过长（%d 字符，上限 %d）", len(rn), noteMaxRunes)
	}
	return nil
}

// validateEntryUnique 规则 4：target 全局唯一（仅 enabled 条目参与比较；
// selfID 非空时排除自身——编辑场景）。
func validateEntryUnique(entries []model.Entry, selfID string) error {
	seen := make(map[string]string)
	for i := range entries {
		if selfID != "" && entries[i].ID == selfID {
			continue
		}
		if !entries[i].Enabled {
			continue
		}
		t := strings.ToLower(strings.TrimSpace(entries[i].Target))
		if t == "" {
			continue
		}
		if first, ok := seen[t]; ok {
			return fmt.Errorf("目标域名重复: %q 同时被条目 %s 与 %s 使用", t, first, entries[i].ID)
		}
		seen[t] = entries[i].ID
	}
	return nil
}

// applyEntryForCommit 提交前归一化（小写、截断）并返回潜在冲突错误。
// 修改入参副本，不触碰内存配置（改动仅在通过校验后由调用方执行）。
func applyEntryForCommit(e *model.Entry) error {
	config.NormalizeEntry(e)
	return validateEntryInput(*e)
}

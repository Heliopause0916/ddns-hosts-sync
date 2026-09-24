package tray

import "testing"

func TestBuildMenuSpec(t *testing.T) {
	cases := []struct {
		name   string
		paused bool
		ids    []string // 期望的完整 ID 序列（确定性顺序）
	}{
		{
			name:   "未暂停：菜单序与勾选态",
			paused: false,
			ids: []string{
				MenuOpenConfig, MenuSyncNow, MenuViewLogs,
				MenuSepBeforePause, MenuPauseToggle,
				MenuSepBeforeQuit, MenuQuit,
			},
		},
		{
			name:   "暂停中：勾选打上",
			paused: true,
			ids: []string{
				MenuOpenConfig, MenuSyncNow, MenuViewLogs,
				MenuSepBeforePause, MenuPauseToggle,
				MenuSepBeforeQuit, MenuQuit,
			},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			specs := BuildMenuSpec(c.paused)
			if len(specs) != len(c.ids) {
				t.Fatalf("菜单规格长度 = %d, want %d", len(specs), len(c.ids))
			}
			for i, id := range c.ids {
				if specs[i].ID != id {
					t.Fatalf("位置 %d ID = %q, want %q", i, specs[i].ID, id)
				}
			}
			// 复选项勾选态与 paused 一致。
			for _, s := range specs {
				if s.ID == MenuPauseToggle {
					if !s.Checkable {
						t.Fatal("暂停项应可复选")
					}
					if s.Checked != c.paused {
						t.Fatalf("暂停项勾选态 = %v, want %v", s.Checked, c.paused)
					}
				} else if s.Checkable {
					t.Fatalf("%s 不应可复选", s.ID)
				}
			}
		})
	}
}

func TestMenuLabel(t *testing.T) {
	if got := Label(MenuOpenConfig); got != "打开配置" {
		t.Fatalf("Label(open_config) = %q", got)
	}
	if got := Label(MenuQuit); got != "退出" {
		t.Fatalf("Label(quit) = %q", got)
	}
	if got := Label("unknown"); got != "unknown" {
		t.Fatalf("未知 ID 应原样返回，得 %q", got)
	}
}

package prompt

import (
	"strings"
	"testing"

	"github.com/n-seiji/ebiii/internal/playbook"
)

func TestBuildPlanPrompt(t *testing.T) {
	tests := []struct {
		name      string
		playbooks []playbook.Playbook
		contains  []string
	}{
		{
			name: "multiple playbooks",
			playbooks: []playbook.Playbook{
				{Name: "Deploy", Description: "Deploy safely", Path: "/absolute/playbooks/deploy.md"},
				{Name: "Review", Description: "Review changes", Path: "/absolute/playbooks/review.md"},
			},
			contains: []string{
				"Deploy", "Deploy safely", "/absolute/playbooks/deploy.md",
				"Review", "Review changes", "/absolute/playbooks/review.md",
			},
		},
		{
			name:      "no playbooks",
			playbooks: nil,
			contains:  []string{"利用可能な playbook はありません"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			const (
				memoryPath = "/absolute/memory/MEMORY.md"
				message    = "この依頼を検討してください"
			)
			got := BuildPlanPrompt(memoryPath, tt.playbooks, message)
			required := append([]string{
				memoryPath,
				"## 方針",
				"## 作業指示",
				"NONE という単独行のみ",
				"<user_message>",
				"</user_message>",
				message,
				"上書きされることはありません",
			}, tt.contains...)
			for _, want := range required {
				if !strings.Contains(got, want) {
					t.Errorf("BuildPlanPrompt() does not contain %q", want)
				}
			}
			if strings.Index(got, "## 方針") >= strings.Index(got, "## 作業指示") {
				t.Error("BuildPlanPrompt() does not mention headings in required order")
			}
		})
	}
}

func TestBuildWorkPrompt(t *testing.T) {
	const (
		instruction = "対象ファイルを更新し、テストを実行する"
		memoryPath  = "/absolute/memory/MEMORY.md"
	)
	got := BuildWorkPrompt(instruction, memoryPath)
	for _, want := range []string{instruction, memoryPath, "重要な学びがあれば", "追記"} {
		if !strings.Contains(got, want) {
			t.Errorf("BuildWorkPrompt() does not contain %q", want)
		}
	}
}

package codex

import (
	"strings"
	"testing"
)

func TestParsePlan(t *testing.T) {
	tests := []struct {
		name            string
		text            string
		wantPolicy      string
		wantInstruction string
		wantErr         bool
	}{
		{
			name:            "valid",
			text:            "preamble\n## 方針\n慎重に進める\n## 作業指示\nテストを追加する",
			wantPolicy:      "慎重に進める",
			wantInstruction: "テストを追加する",
		},
		{
			name:    "policy only",
			text:    "## 方針\n慎重に進める",
			wantErr: true,
		},
		{
			name:    "instruction only",
			text:    "## 作業指示\nテストを追加する",
			wantErr: true,
		},
		{
			name:    "wrong order",
			text:    "## 作業指示\n作業\n## 方針\n方針",
			wantErr: true,
		},
		{
			name:    "duplicate policy",
			text:    "## 方針\n一\n## 方針\n二\n## 作業指示\n作業",
			wantErr: true,
		},
		{
			name:    "duplicate instruction",
			text:    "## 方針\n方針\n## 作業指示\n一\n## 作業指示\n二",
			wantErr: true,
		},
		{
			name:    "empty policy",
			text:    "## 方針\n\n## 作業指示\n作業",
			wantErr: true,
		},
		{
			name:    "empty instruction",
			text:    "## 方針\n方針\n## 作業指示\n  ",
			wantErr: true,
		},
		{
			name: "heading-like text in closed fences",
			text: strings.Join([]string{
				"```markdown", "## 方針", "偽物", "```",
				"## 方針", "本物", "## 作業指示", "作業",
				"~~~", "## 作業指示", "偽物", "~~~",
			}, "\n"),
			wantPolicy:      "本物",
			wantInstruction: "作業\n~~~\n## 作業指示\n偽物\n~~~",
		},
		{
			name: "heading-like text in unclosed fence",
			text: strings.Join([]string{
				"## 方針", "本物", "## 作業指示", "作業",
				"```", "## 方針", "偽物",
			}, "\n"),
			wantPolicy:      "本物",
			wantInstruction: "作業\n```\n## 方針\n偽物",
		},
		{
			name:            "heading not at line start",
			text:            "文章中の ## 方針 は偽物\n## 方針\n本物\n## 作業指示\n作業",
			wantPolicy:      "本物",
			wantInstruction: "作業",
		},
		{
			name:            "surrounding heading whitespace",
			text:            "  ## 方針  \n方針\n\t## 作業指示 \t\n作業",
			wantPolicy:      "方針",
			wantInstruction: "作業",
		},
		{
			name:            "NONE alone",
			text:            "## 方針\n何もしない\n## 作業指示\nNONE",
			wantPolicy:      "何もしない",
			wantInstruction: "",
		},
		{
			name:            "NONE with commentary is ordinary instruction",
			text:            "## 方針\n確認する\n## 作業指示\nNONE\nただし後で確認",
			wantPolicy:      "確認する",
			wantInstruction: "NONE\nただし後で確認",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			policy, instruction, err := ParsePlan(tt.text)
			if tt.wantErr {
				if err == nil {
					t.Fatalf("ParsePlan() = (%q, %q, nil), want error", policy, instruction)
				}
				if policy != "" || instruction != "" {
					t.Errorf("ParsePlan() on error = (%q, %q), want empty values", policy, instruction)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParsePlan() error = %v", err)
			}
			if policy != tt.wantPolicy || instruction != tt.wantInstruction {
				t.Errorf("ParsePlan() = (%q, %q), want (%q, %q)",
					policy, instruction, tt.wantPolicy, tt.wantInstruction)
			}
		})
	}
}

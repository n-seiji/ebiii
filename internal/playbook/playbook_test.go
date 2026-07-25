package playbook

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestListFiltersPlaybooks(t *testing.T) {
	tests := []struct {
		name    string
		content string
		want    []Playbook
	}{
		{
			name:    "valid frontmatter",
			content: "---\nname: Deploy\ndescription: Deploy the application\n---\n# Steps\n",
			want: []Playbook{{
				Name:        "Deploy",
				Description: "Deploy the application",
			}},
		},
		{
			name:    "missing frontmatter",
			content: "# README\n",
			want:    []Playbook{},
		},
		{
			name:    "missing name",
			content: "---\ndescription: Has no name\n---\n",
			want:    []Playbook{},
		},
		{
			name:    "missing description",
			content: "---\nname: Incomplete\n---\n",
			want:    []Playbook{},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := t.TempDir()
			path := filepath.Join(dir, "playbook.md")
			if err := os.WriteFile(path, []byte(tt.content), 0o600); err != nil {
				t.Fatalf("WriteFile() error = %v", err)
			}

			got, err := List(dir)
			if err != nil {
				t.Fatalf("List() error = %v", err)
			}
			if len(got) != len(tt.want) {
				t.Fatalf("List() returned %d playbooks, want %d", len(got), len(tt.want))
			}
			for i := range got {
				if got[i].Name != tt.want[i].Name || got[i].Description != tt.want[i].Description {
					t.Errorf("List()[%d] = %#v, want name %q and description %q",
						i, got[i], tt.want[i].Name, tt.want[i].Description)
				}
				if !filepath.IsAbs(got[i].Path) {
					t.Errorf("List()[%d].Path = %q, want absolute path", i, got[i].Path)
				}
			}
		})
	}
}

func TestListSortsByFilename(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"z-last.md":  "---\nname: Last\ndescription: Last by filename\n---\n",
		"a-first.md": "---\nname: First\ndescription: First by filename\n---\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o600); err != nil {
			t.Fatalf("WriteFile(%q) error = %v", name, err)
		}
	}

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 2 {
		t.Fatalf("List() returned %d playbooks, want 2", len(got))
	}
	if got[0].Name != "First" || got[1].Name != "Last" {
		t.Errorf("List() names = [%q, %q], want [First, Last]", got[0].Name, got[1].Name)
	}
}

func TestListExcludesOversizedFile(t *testing.T) {
	dir := t.TempDir()
	content := "---\nname: Large\ndescription: Too large\n---\n" + strings.Repeat("x", maxFileSize)
	if err := os.WriteFile(filepath.Join(dir, "large.md"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := List(dir)
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if len(got) != 0 {
		t.Errorf("List() returned %d playbooks, want 0", len(got))
	}
}

func TestListEmptyDirectory(t *testing.T) {
	got, err := List(t.TempDir())
	if err != nil {
		t.Fatalf("List() error = %v", err)
	}
	if got == nil {
		t.Fatal("List() returned nil, want empty slice")
	}
	if len(got) != 0 {
		t.Errorf("List() returned %d playbooks, want 0", len(got))
	}
}

package config

import (
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestLoad(t *testing.T) {
	tests := []struct {
		name    string
		env     map[string]string
		wantErr string
		check   func(*testing.T, *Config, string)
	}{
		{
			name: "valid",
			env: map[string]string{
				"SLACK_BOT_TOKEN":           "xoxb-test",
				"SLACK_APP_TOKEN":           "xapp-test",
				"SLACK_ALLOWED_USER_IDS":    " U123, ,U456 ",
				"SLACK_ALLOWED_CHANNEL_IDS": " C123, ,C456 ",
				"CODEX_COMMAND":             "/usr/local/bin/codex",
				"CODEX_MODEL":               "gpt-test",
				"CODEX_TIMEOUT":             "45s",
				"EBIII_HOME":                "data",
			},
			check: func(t *testing.T, cfg *Config, workingDir string) {
				t.Helper()
				wantHome := filepath.Join(workingDir, "data")
				if !reflect.DeepEqual(cfg.AllowedUserIDs, []string{"U123", "U456"}) {
					t.Errorf("AllowedUserIDs = %v", cfg.AllowedUserIDs)
				}
				if !reflect.DeepEqual(cfg.AllowedChannelIDs, []string{"C123", "C456"}) {
					t.Errorf("AllowedChannelIDs = %v", cfg.AllowedChannelIDs)
				}
				if cfg.CodexCommand != "/usr/local/bin/codex" {
					t.Errorf("CodexCommand = %q", cfg.CodexCommand)
				}
				if cfg.CodexModel != "gpt-test" {
					t.Errorf("CodexModel = %q", cfg.CodexModel)
				}
				if cfg.CodexTimeout != 45*time.Second {
					t.Errorf("CodexTimeout = %v", cfg.CodexTimeout)
				}
				if cfg.EBIIIHome != wantHome {
					t.Errorf("EBIIIHome = %q, want %q", cfg.EBIIIHome, wantHome)
				}
				assertPaths(t, cfg, wantHome)
			},
		},
		{
			name: "missing bot token",
			env: map[string]string{
				"SLACK_APP_TOKEN":        "xapp-test",
				"SLACK_ALLOWED_USER_IDS": "U123",
			},
			wantErr: "SLACK_BOT_TOKEN",
		},
		{
			name: "empty allowed users",
			env: map[string]string{
				"SLACK_BOT_TOKEN":        "xoxb-test",
				"SLACK_APP_TOKEN":        "xapp-test",
				"SLACK_ALLOWED_USER_IDS": " , ",
			},
			wantErr: "SLACK_ALLOWED_USER_IDS",
		},
		{
			name: "invalid allowed user among valid users",
			env: map[string]string{
				"SLACK_BOT_TOKEN":        "xoxb-test",
				"SLACK_APP_TOKEN":        "xapp-test",
				"SLACK_ALLOWED_USER_IDS": "U123,W456",
			},
			wantErr: `SLACK_ALLOWED_USER_IDS: invalid user ID "W456"`,
		},
		{
			name: "default home and optional values",
			env: map[string]string{
				"SLACK_BOT_TOKEN":        "xoxb-test",
				"SLACK_APP_TOKEN":        "xapp-test",
				"SLACK_ALLOWED_USER_IDS": "U123",
			},
			check: func(t *testing.T, cfg *Config, workingDir string) {
				t.Helper()
				if cfg.EBIIIHome != workingDir {
					t.Errorf("EBIIIHome = %q, want %q", cfg.EBIIIHome, workingDir)
				}
				if cfg.CodexCommand != "codex" {
					t.Errorf("CodexCommand = %q", cfg.CodexCommand)
				}
				if cfg.CodexTimeout != 30*time.Minute {
					t.Errorf("CodexTimeout = %v", cfg.CodexTimeout)
				}
				if len(cfg.AllowedChannelIDs) != 0 {
					t.Errorf("AllowedChannelIDs = %v", cfg.AllowedChannelIDs)
				}
				assertPaths(t, cfg, workingDir)
			},
		},
		{
			name: "invalid timeout",
			env: map[string]string{
				"SLACK_BOT_TOKEN":        "xoxb-test",
				"SLACK_APP_TOKEN":        "xapp-test",
				"SLACK_ALLOWED_USER_IDS": "U123",
				"CODEX_TIMEOUT":          "tomorrow",
			},
			wantErr: "CODEX_TIMEOUT",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			workingDir := t.TempDir()
			withWorkingDir(t, workingDir)
			workingDir, err := filepath.Abs(".")
			if err != nil {
				t.Fatalf("resolve working directory: %v", err)
			}
			clearConfigEnv(t)
			for key, value := range tt.env {
				t.Setenv(key, value)
			}

			cfg, err := Load()
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("Load() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			tt.check(t, cfg, workingDir)
		})
	}
}

func TestLoadDotEnv(t *testing.T) {
	workingDir := t.TempDir()
	withWorkingDir(t, workingDir)
	clearConfigEnv(t)
	t.Setenv("SLACK_BOT_TOKEN", "from-environment")

	content := "SLACK_BOT_TOKEN=from-file\n" +
		"SLACK_APP_TOKEN='xapp-file'\n" +
		"export SLACK_ALLOWED_USER_IDS=\"U123,U456\"\n"
	if err := os.WriteFile(filepath.Join(workingDir, ".env"), []byte(content), 0o600); err != nil {
		t.Fatalf("write .env: %v", err)
	}

	cfg, err := Load()
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if cfg.SlackBotToken != "from-environment" {
		t.Errorf("SlackBotToken = %q", cfg.SlackBotToken)
	}
	if cfg.SlackAppToken != "xapp-file" {
		t.Errorf("SlackAppToken = %q", cfg.SlackAppToken)
	}
	if !reflect.DeepEqual(cfg.AllowedUserIDs, []string{"U123", "U456"}) {
		t.Errorf("AllowedUserIDs = %v", cfg.AllowedUserIDs)
	}
}

func assertPaths(t *testing.T, cfg *Config, home string) {
	t.Helper()
	paths := map[string]string{
		"WorkspaceDir": cfg.WorkspaceDir,
		"MemoryDir":    cfg.MemoryDir,
		"PlaybooksDir": cfg.PlaybooksDir,
		"StateDir":     cfg.StateDir,
	}
	for name, got := range paths {
		want := filepath.Join(home, strings.TrimSuffix(name, "Dir"))
		want = filepath.Join(home, strings.ToLower(filepath.Base(want)))
		if got != want {
			t.Errorf("%s = %q, want %q", name, got, want)
		}
	}
}

func clearConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range []string{
		"SLACK_BOT_TOKEN",
		"SLACK_APP_TOKEN",
		"SLACK_ALLOWED_USER_IDS",
		"SLACK_ALLOWED_CHANNEL_IDS",
		"CODEX_COMMAND",
		"CODEX_MODEL",
		"CODEX_TIMEOUT",
		"EBIII_HOME",
	} {
		value, exists := os.LookupEnv(key)
		if err := os.Unsetenv(key); err != nil {
			t.Fatalf("unset %s: %v", key, err)
		}
		t.Cleanup(func() {
			if exists {
				_ = os.Setenv(key, value)
				return
			}
			_ = os.Unsetenv(key)
		})
	}
}

func withWorkingDir(t *testing.T, dir string) {
	t.Helper()
	previous, err := os.Getwd()
	if err != nil {
		t.Fatalf("get working directory: %v", err)
	}
	if err := os.Chdir(dir); err != nil {
		t.Fatalf("change working directory: %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chdir(previous)
	})
}

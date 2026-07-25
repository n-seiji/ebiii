// Package config loads ebiii's runtime configuration.
package config

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const defaultCodexTimeout = 30 * time.Minute

// Config contains ebiii's runtime configuration and resolved data paths.
type Config struct {
	SlackBotToken     string
	SlackAppToken     string
	AllowedUserIDs    []string
	AllowedChannelIDs []string
	CodexCommand      string
	CodexModel        string
	CodexTimeout      time.Duration
	EBIIIHome         string
	WorkspaceDir      string
	MemoryDir         string
	PlaybooksDir      string
	StateDir          string
}

// Load reads configuration from the environment and a local .env file.
//
// Values already present in the process environment take precedence over
// values in .env.
func Load() (*Config, error) {
	if err := loadDotEnv(".env"); err != nil {
		return nil, fmt.Errorf("load .env: %w", err)
	}

	botToken, err := requiredEnv("SLACK_BOT_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("SLACK_BOT_TOKEN: %w", err)
	}
	appToken, err := requiredEnv("SLACK_APP_TOKEN")
	if err != nil {
		return nil, fmt.Errorf("SLACK_APP_TOKEN: %w", err)
	}

	userIDs := splitList(os.Getenv("SLACK_ALLOWED_USER_IDS"))
	if len(userIDs) == 0 {
		return nil, fmt.Errorf("SLACK_ALLOWED_USER_IDS: %w", errors.New("must contain at least one user ID"))
	}
	for _, userID := range userIDs {
		if !strings.HasPrefix(userID, "U") {
			return nil, fmt.Errorf("SLACK_ALLOWED_USER_IDS: invalid user ID %q: %w", userID, errors.New("must start with U"))
		}
	}

	codexCommand := strings.TrimSpace(os.Getenv("CODEX_COMMAND"))
	if codexCommand == "" {
		codexCommand = "codex"
	}

	codexTimeout := defaultCodexTimeout
	if value := strings.TrimSpace(os.Getenv("CODEX_TIMEOUT")); value != "" {
		codexTimeout, err = time.ParseDuration(value)
		if err != nil {
			return nil, fmt.Errorf("CODEX_TIMEOUT %q: %w", value, err)
		}
	}

	home := strings.TrimSpace(os.Getenv("EBIII_HOME"))
	if home == "" {
		home = "."
	}
	absoluteHome, err := filepath.Abs(home)
	if err != nil {
		return nil, fmt.Errorf("EBIII_HOME %q: %w", home, err)
	}
	home = absoluteHome

	return &Config{
		SlackBotToken:     botToken,
		SlackAppToken:     appToken,
		AllowedUserIDs:    userIDs,
		AllowedChannelIDs: splitList(os.Getenv("SLACK_ALLOWED_CHANNEL_IDS")),
		CodexCommand:      codexCommand,
		CodexModel:        strings.TrimSpace(os.Getenv("CODEX_MODEL")),
		CodexTimeout:      codexTimeout,
		EBIIIHome:         home,
		WorkspaceDir:      filepath.Join(home, "workspace"),
		MemoryDir:         filepath.Join(home, "memory"),
		PlaybooksDir:      filepath.Join(home, "playbooks"),
		StateDir:          filepath.Join(home, "state"),
	}, nil
}

func requiredEnv(name string) (string, error) {
	value := strings.TrimSpace(os.Getenv(name))
	if value == "" {
		return "", errors.New("is required")
	}
	return value, nil
}

func splitList(value string) []string {
	var result []string
	for _, item := range strings.Split(value, ",") {
		item = strings.TrimSpace(item)
		if item != "" {
			result = append(result, item)
		}
	}
	return result
}

func loadDotEnv(path string) error {
	file, err := os.Open(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("open %s: %w", path, err)
	}
	defer file.Close()

	scanner := bufio.NewScanner(file)
	for lineNumber := 1; scanner.Scan(); lineNumber++ {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}

		line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		key, value, found := strings.Cut(line, "=")
		key = strings.TrimSpace(key)
		if !found || key == "" {
			return fmt.Errorf("parse %s line %d: %w", path, lineNumber, errors.New("expected KEY=VALUE"))
		}
		value = strings.TrimSpace(value)
		if len(value) >= 2 {
			if (value[0] == '"' && value[len(value)-1] == '"') ||
				(value[0] == '\'' && value[len(value)-1] == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if _, exists := os.LookupEnv(key); exists {
			continue
		}
		if err := os.Setenv(key, value); err != nil {
			return fmt.Errorf("set %s from %s: %w", key, path, err)
		}
	}
	if err := scanner.Err(); err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	return nil
}

// Package memory reads and appends the persistent MEMORY.md file. The Codex
// agent never writes this file directly; the bot validates proposed entries
// and appends them itself so Slack input cannot rewrite accumulated memory.
package memory

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// FileName is the memory file managed inside the memory directory.
const FileName = "MEMORY.md"

const (
	// maxPromptBytes caps how much memory content is injected into prompts.
	maxPromptBytes = 16 << 10
	// MaxAppendBytes caps the size of one appended entry.
	MaxAppendBytes = 4 << 10
)

// Read returns the memory file content capped at maxPromptBytes. A missing
// file yields an empty string without error.
func Read(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, FileName))
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", err
	}
	text := string(data)
	if len(text) > maxPromptBytes {
		text = truncateRunes(text, maxPromptBytes) + "\n(以降はサイズ上限のため省略)"
	}
	return strings.TrimSpace(text), nil
}

// Append validates and appends one entry to the memory file, creating the
// directory and file when needed. It returns the entry actually written.
//
// Append does not serialize concurrent callers; callers must do so themselves
// (the Slack bot holds its memory lock around the call). When Close reports an
// error the entry may already be on disk, but Append still returns the error
// and an empty entry; it never retries the write on its own.
func Append(dir, entry string) (written string, err error) {
	entry = strings.TrimSpace(entry)
	if entry == "" {
		return "", nil
	}
	if len(entry) > MaxAppendBytes {
		entry = strings.TrimSpace(truncateRunes(entry, MaxAppendBytes)) + "\n(サイズ上限のため切り詰め)"
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	file, err := os.OpenFile(filepath.Join(dir, FileName), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return "", err
	}
	defer func() {
		if closeErr := file.Close(); closeErr != nil && err == nil {
			written, err = "", closeErr
		}
	}()
	if _, err := file.WriteString("\n" + entry + "\n"); err != nil {
		return "", err
	}
	return entry, nil
}

func truncateRunes(text string, limit int) string {
	if len(text) <= limit {
		return text
	}
	end := limit
	for end > 0 && !utf8.RuneStart(text[end]) {
		end--
	}
	return text[:end]
}

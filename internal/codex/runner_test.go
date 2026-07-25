package codex

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func TestBuildArgs(t *testing.T) {
	tests := []struct {
		name          string
		threadID      string
		sandbox       string
		cwd           string
		writableRoots []string
		model         string
		instructions  string
		want          []string
	}{
		{
			name:    "new read-only without model",
			sandbox: "read-only",
			cwd:     "/work",
			want: []string{
				"exec", "--json", "--skip-git-repo-check",
				"-c", `sandbox_mode="read-only"`,
				"-c", `approval_policy="never"`,
				"-C", "/work", "-",
			},
		},
		{
			name:         "developer instructions are quoted",
			sandbox:      "read-only",
			cwd:          "/work",
			instructions: "絶対ルール\n\"quoted\"",
			want: []string{
				"exec", "--json", "--skip-git-repo-check",
				"-c", `sandbox_mode="read-only"`,
				"-c", `approval_policy="never"`,
				"-c", `developer_instructions="絶対ルール\n\"quoted\""`,
				"-C", "/work", "-",
			},
		},
		{
			name:          "new workspace-write with roots and model",
			sandbox:       "workspace-write",
			cwd:           "/work",
			writableRoots: []string{"/memory", `/a"b`},
			model:         "gpt-test",
			want: []string{
				"exec", "--json", "--skip-git-repo-check",
				"-c", `sandbox_mode="workspace-write"`,
				"-c", `approval_policy="never"`,
				"-c", "sandbox_workspace_write.network_access=true",
				"-c", `sandbox_workspace_write.writable_roots=["/memory","/a\"b"]`,
				"-m", "gpt-test", "-C", "/work", "-",
			},
		},
		{
			name:          "read-only ignores roots",
			sandbox:       "read-only",
			cwd:           "/work",
			writableRoots: []string{"/memory"},
			want: []string{
				"exec", "--json", "--skip-git-repo-check",
				"-c", `sandbox_mode="read-only"`,
				"-c", `approval_policy="never"`,
				"-C", "/work", "-",
			},
		},
		{
			name:          "resume omits cwd",
			threadID:      "thread-1",
			sandbox:       "workspace-write",
			cwd:           "/ignored",
			writableRoots: []string{"/memory"},
			model:         "gpt-test",
			want: []string{
				"exec", "resume", "thread-1", "--json", "--skip-git-repo-check",
				"-c", `sandbox_mode="workspace-write"`,
				"-c", `approval_policy="never"`,
				"-c", "sandbox_workspace_write.network_access=true",
				"-c", `sandbox_workspace_write.writable_roots=["/memory"]`,
				"-m", "gpt-test", "-",
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := buildArgs(tt.threadID, tt.sandbox, tt.cwd, tt.writableRoots, tt.model, tt.instructions)
			if !reflect.DeepEqual(got, tt.want) {
				t.Fatalf("buildArgs() = %#v, want %#v", got, tt.want)
			}
		})
	}
}

func TestParseJSONL(t *testing.T) {
	messages101 := strings.Repeat(
		"{\"type\":\"item.completed\",\"item\":{\"type\":\"agent_message\",\"text\":\"x\"}}\n",
		maxMessages+1,
	)
	tests := []struct {
		name          string
		output        string
		want          TurnResult
		wantErr       string
		wantCallbacks []string
	}{
		{
			name: "completed",
			output: strings.Join([]string{
				`{"type":"thread.started","thread_id":"thread-1"}`,
				`{"type":"item.completed","item":{"type":"agent_message","text":"one"}}`,
				`{"type":"item.completed","item":{"type":"agent_message","text":"two"}}`,
				`{"type":"turn.completed"}`,
			}, "\n"),
			want: TurnResult{
				ThreadID:  "thread-1",
				Messages:  []string{"one", "two"},
				Completed: true,
			},
			wantCallbacks: []string{"thread-1"},
		},
		{
			name:   "failed",
			output: `{"type":"turn.failed","error":{"message":"bad turn"}}`,
			want:   TurnResult{Err: "bad turn"},
		},
		{
			name:   "error event",
			output: `{"type":"error","message":"transport failed"}`,
			want:   TurnResult{Err: "transport failed"},
		},
		{
			name:    "malformed JSON",
			output:  "{broken",
			wantErr: "parse codex jsonl",
		},
		{
			name: "unknown event ignored",
			output: strings.Join([]string{
				`{"type":"future.event","value":1}`,
				`{"type":"turn.completed"}`,
			}, "\n"),
			want: TurnResult{Completed: true},
		},
		{
			name:   "completed without messages",
			output: `{"type":"turn.completed"}`,
			want:   TurnResult{Completed: true},
		},
		{
			name:    "message count exceeded",
			output:  messages101,
			wantErr: "agent_message count exceeds 100",
		},
		{
			name: "failure overrides completion",
			output: strings.Join([]string{
				`{"type":"turn.completed"}`,
				`{"type":"turn.failed","error":{"message":"late failure"}}`,
			}, "\n"),
			want: TurnResult{Err: "late failure"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := TurnResult{}
			var callbacks []string
			err := parseJSONL(strings.NewReader(tt.output), &got, func(id string) error {
				callbacks = append(callbacks, id)
				return nil
			})
			if tt.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), tt.wantErr) {
					t.Fatalf("parseJSONL() error = %v, want containing %q", err, tt.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("parseJSONL() error = %v", err)
			}
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("result = %#v, want %#v", got, tt.want)
			}
			if !reflect.DeepEqual(callbacks, tt.wantCallbacks) {
				t.Errorf("callbacks = %#v, want %#v", callbacks, tt.wantCallbacks)
			}
		})
	}
}

func TestParseJSONLStdoutLimit(t *testing.T) {
	output := strings.Repeat(" ", maxStdoutBytes+1)
	var got TurnResult
	err := parseJSONL(strings.NewReader(output), &got, nil)
	if err == nil || !strings.Contains(err.Error(), "stdout exceeds 64MB") {
		t.Fatalf("parseJSONL() error = %v, want stdout limit error", err)
	}
}

func TestTruncateFinalMessage(t *testing.T) {
	prefix := strings.Repeat("a", maxFinalBytes-1)
	result := &TurnResult{Messages: []string{"earlier", prefix + "界"}}
	truncateFinalMessage(result)
	got := result.Messages[1]
	if len(got) > maxFinalBytes {
		t.Fatalf("final message length = %d, want <= %d", len(got), maxFinalBytes)
	}
	if !utf8.ValidString(got) {
		t.Fatal("final message is not valid UTF-8")
	}
	if result.Messages[0] != "earlier" {
		t.Errorf("earlier message = %q, want unchanged", result.Messages[0])
	}
}

func TestRunThreadStartedCallbackErrorCancelsProcess(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	content := `#!/bin/sh
cat >/dev/null
printf '{"type":"thread.started","thread_id":"thread-1"}\n'
while :; do :; done
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	runner := &Runner{Command: script}
	start := time.Now()
	_, err := runner.Run(
		context.Background(), "", "read-only", dir, nil, "prompt",
		func(string) error { return errors.New("persist failed") },
	)
	if err == nil || !strings.Contains(err.Error(), "persist failed") {
		t.Fatalf("Run() error = %v, want wrapped callback error", err)
	}
	if time.Since(start) > 5*time.Second {
		t.Fatal("Run() did not wait for and promptly terminate the child process")
	}
}

func TestRunReturnsTurnFailureResult(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	content := `#!/bin/sh
cat >/dev/null
printf '{"type":"turn.failed","error":{"message":"bad turn"}}\n'
printf 'diagnostic\n' >&2
exit 1
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	runner := &Runner{Command: script}
	result, err := runner.Run(context.Background(), "", "read-only", dir, nil, "prompt", nil)
	if err != nil {
		t.Fatalf("Run() error = %v, want semantic failure in TurnResult", err)
	}
	if result.Completed || result.Err != "bad turn" {
		t.Fatalf("Run() result = %#v, want incomplete bad turn", result)
	}
}

func TestRunContextCancellationWaitsForProcess(t *testing.T) {
	dir := t.TempDir()
	script := filepath.Join(dir, "codex")
	content := `#!/bin/sh
cat >/dev/null
while :; do :; done
`
	if err := os.WriteFile(script, []byte(content), 0o755); err != nil {
		t.Fatalf("write fake codex: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	runner := &Runner{Command: script}
	_, err := runner.Run(ctx, "", "read-only", dir, nil, "prompt", nil)
	if err == nil || !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("Run() error = %v, want deadline exceeded", err)
	}
}

func TestLimitedBufferTruncates(t *testing.T) {
	buffer := &limitedBuffer{limit: 4}
	n, err := fmt.Fprint(buffer, "abcdef")
	if err != nil {
		t.Fatalf("write: %v", err)
	}
	if n != 6 || buffer.String() != "abcd" {
		t.Fatalf("write = (%d, %q), want (6, %q)", n, buffer.String(), "abcd")
	}
}

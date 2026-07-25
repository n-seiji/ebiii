// Package policy embeds the operator-controlled rules that are injected into
// every Codex turn as developer instructions. The content is compiled into
// the binary so it cannot be modified from Slack, memory, or the sandboxed
// filesystem.
package policy

import (
	_ "embed"
	"strings"
)

//go:embed policy.md
var instructions string

// Instructions returns the embedded developer instructions.
func Instructions() string {
	return strings.TrimSpace(instructions)
}

package codex

import (
	"errors"
	"strings"
)

const (
	policyHeading      = "## 方針"
	instructionHeading = "## 作業指示"
)

// ParsePlan extracts the policy and work instruction from a plan response.
func ParsePlan(text string) (string, string, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var policyIndexes, instructionIndexes []int
	inFence := false
	var fence byte

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if marker, ok := fenceMarker(trimmed); ok {
			if !inFence {
				inFence = true
				fence = marker
			} else if marker == fence {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		switch trimmed {
		case policyHeading:
			policyIndexes = append(policyIndexes, i)
		case instructionHeading:
			instructionIndexes = append(instructionIndexes, i)
		}
	}

	if len(policyIndexes) != 1 || len(instructionIndexes) != 1 {
		return "", "", errors.New("plan must contain exactly one policy and one instruction heading")
	}
	policyIndex := policyIndexes[0]
	instructionIndex := instructionIndexes[0]
	if policyIndex >= instructionIndex {
		return "", "", errors.New("policy heading must precede instruction heading")
	}

	policy := strings.TrimSpace(strings.Join(lines[policyIndex+1:instructionIndex], "\n"))
	instruction := strings.TrimSpace(strings.Join(lines[instructionIndex+1:], "\n"))
	if policy == "" || instruction == "" {
		return "", "", errors.New("plan sections must not be empty")
	}
	if instruction == "NONE" {
		instruction = ""
	}
	return policy, instruction, nil
}

func fenceMarker(line string) (byte, bool) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, false
	}
	marker := line[0]
	count := 0
	for count < len(line) && line[count] == marker {
		count++
	}
	return marker, count >= 3
}

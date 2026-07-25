package codex

import (
	"errors"
	"strings"
)

const (
	policyHeading      = "## 方針"
	instructionHeading = "## 作業指示"
	memoryHeading      = "## メモリ追記"
)

// ParsePlan extracts the policy and work instruction from a plan response.
func ParsePlan(text string) (string, string, error) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var policyIndexes, instructionIndexes []int
	inFence := false
	var fence byte
	var fenceLen int

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if marker, length, ok := fenceMarker(trimmed); ok {
			if !inFence {
				inFence = true
				fence = marker
				fenceLen = length
			} else if marker == fence && length >= fenceLen {
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

// SplitMemoryAppend separates an optional trailing memory-append section from
// a work response. When the section is absent or ambiguous (zero or multiple
// headings), the original text is returned unchanged with an empty entry so
// no memory write happens.
func SplitMemoryAppend(text string) (rest, entry string) {
	lines := strings.Split(strings.ReplaceAll(text, "\r\n", "\n"), "\n")
	var indexes []int
	inFence := false
	var fence byte
	var fenceLen int

	for i, line := range lines {
		trimmed := strings.TrimSpace(line)
		if marker, length, ok := fenceMarker(trimmed); ok {
			if !inFence {
				inFence = true
				fence = marker
				fenceLen = length
			} else if marker == fence && length >= fenceLen {
				inFence = false
			}
			continue
		}
		if inFence {
			continue
		}
		if trimmed == memoryHeading {
			indexes = append(indexes, i)
		}
	}

	if len(indexes) != 1 {
		return text, ""
	}
	index := indexes[0]
	rest = strings.TrimSpace(strings.Join(lines[:index], "\n"))
	entry = strings.TrimSpace(strings.Join(lines[index+1:], "\n"))
	return rest, entry
}

// fenceMarker reports the fence character and the number of times it is
// repeated at the start of line. A closing fence must use the same character
// as its opening fence and be at least as long.
func fenceMarker(line string) (char byte, length int, ok bool) {
	if len(line) < 3 || (line[0] != '`' && line[0] != '~') {
		return 0, 0, false
	}
	char = line[0]
	for length < len(line) && line[length] == char {
		length++
	}
	if length < 3 {
		return 0, 0, false
	}
	return char, length, true
}

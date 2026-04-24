package main

import (
	"regexp"
	"strings"
)

var (
	ansiCursorPattern = regexp.MustCompile(`\x1b\[[\d;]*[ABCDEFGHJKfsu]`)
	ansiEscapePattern = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~]|\][^\x07]*\x07)`)
	controlChars      = regexp.MustCompile(`[\x00-\x08\x0B-\x0C\x0E-\x1F]`)
	multiSpace        = regexp.MustCompile(` +`)
	yesNoPattern      = regexp.MustCompile(`[23][\.\)]\s*No|•\s*No`)
	ynAtEnd           = regexp.MustCompile(`\(y/n\)\s*$`)
	needsYesPattern   = regexp.MustCompile(`(?i)Type.*yes|Enter.*yes|\(y/n\)`)
)

// StripANSI removes ANSI escape codes from text, replacing cursor movements with spaces.
func StripANSI(text string) string {
	text = ansiCursorPattern.ReplaceAllString(text, " ")
	text = ansiEscapePattern.ReplaceAllString(text, "")
	text = controlChars.ReplaceAllString(text, "")
	text = strings.ReplaceAll(text, "\r", " ")
	text = multiSpace.ReplaceAllString(text, " ")
	return text
}

// IsPrompt detects if text is a Claude Code permission prompt.
// Only the last 50 lines are examined — the dialog is always the most recent output.
// Returns (detected bool, score int).
func IsPrompt(text string) (bool, int) {
	clean := StripANSI(text)

	lines := strings.Split(clean, "\n")
	const tailLines = 50
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	tail := strings.Join(lines, "\n")

	score := 0
	var matched []string

	hasYes := strings.Contains(tail, "1. Yes") || strings.Contains(tail, "1) Yes") || strings.Contains(tail, "• Yes")
	hasNo := yesNoPattern.MatchString(tail)
	if hasYes && hasNo {
		score += 5
		matched = append(matched, "yes_no_buttons")
	}

	if strings.Contains(tail, "Enter to approve") || strings.Contains(tail, "Enter to confirm") {
		score += 3
		matched = append(matched, "enter_to_approve")
	}

	if strings.Contains(tail, "Esc to cancel") {
		score += 2
		matched = append(matched, "esc_to_cancel")
	}

	if strings.Contains(tail, "Tab to amend") {
		score += 2
		matched = append(matched, "tab_to_amend")
	}

	if ynAtEnd.MatchString(tail) {
		score += 3
		matched = append(matched, "yn_prompt")
	}

	if strings.Contains(tail, "Permission rule") {
		score += 3
		matched = append(matched, "permission_rule")
	}

	if score > 0 {
		logf("DETECTION: score=%d indicators=%v", score, matched)
	}

	return score >= 3, score
}

// NeedsYes returns true if the prompt requires typing "yes" rather than just Enter.
func NeedsYes(text string) bool {
	return needsYesPattern.MatchString(StripANSI(text))
}

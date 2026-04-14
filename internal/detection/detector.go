package detection

import (
	"regexp"
	"strings"

	"github.com/alehatsman/claude-autoapprove/internal/debug"
)

// ANSI escape code patterns
var ansiCursorPattern = regexp.MustCompile(`\x1b\[[\d;]*[ABCDEFGHJKfsu]`)                          // Cursor movements - replace with space
var ansiEscapePattern = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~]|\][^\x07]*\x07)`) // All ANSI codes
var controlChars = regexp.MustCompile(`[\x00-\x08\x0B-\x0C\x0E-\x1F]`)                             // Control chars except \t, \n, \r

// StripANSI removes ANSI escape codes from text, replacing cursor movements with spaces
func StripANSI(text string) string {
	// First replace cursor movement codes with spaces (KEY INSIGHT from Python version)
	text = ansiCursorPattern.ReplaceAllString(text, " ")

	// Then remove all other ANSI codes
	text = ansiEscapePattern.ReplaceAllString(text, "")

	// Remove control characters except tab, newline, carriage return
	text = controlChars.ReplaceAllString(text, "")

	// Normalize whitespace: replace carriage returns with spaces
	text = strings.ReplaceAll(text, "\r", " ")

	// Clean up multiple spaces (like Python version does)
	multiSpace := regexp.MustCompile(` +`)
	text = multiSpace.ReplaceAllString(text, " ")

	return text
}

// IsPrompt detects if text is a permission prompt using UI element detection.
// Only the last 50 lines are examined — the permission dialog is always the most
// recent output, so earlier content (code, comments, backticks) is irrelevant.
// Returns (isPrompt bool, score int)
func IsPrompt(text string) (bool, int) {
	clean := StripANSI(text)

	// Take the last 50 lines — the dialog is always at the tail
	lines := strings.Split(clean, "\n")
	const tailLines = 50
	if len(lines) > tailLines {
		lines = lines[len(lines)-tailLines:]
	}
	tail := strings.Join(lines, "\n")

	score := 0
	matchedIndicators := []string{}

	// 1. YES/NO BUTTONS (strongest indicator - Claude Code's button UI)
	hasYesButton := strings.Contains(tail, "1. Yes") ||
		strings.Contains(tail, "1) Yes") ||
		strings.Contains(tail, "• Yes")

	yesNoPattern := regexp.MustCompile(`[23][\.\)]\s*No|•\s*No`)
	hasNoButton := yesNoPattern.MatchString(tail)

	if hasYesButton && hasNoButton {
		score += 5
		matchedIndicators = append(matchedIndicators, "yes_no_buttons")
	}

	// 2. ENTER TO APPROVE / CONFIRM (primary action UI)
	if strings.Contains(tail, "Enter to approve") || strings.Contains(tail, "Enter to confirm") {
		score += 3
		matchedIndicators = append(matchedIndicators, "enter_to_approve")
	}

	// 3. ESC TO CANCEL (secondary action UI)
	if strings.Contains(tail, "Esc to cancel") {
		score += 2
		matchedIndicators = append(matchedIndicators, "esc_to_cancel")
	}

	// 4. TAB TO AMEND (tertiary action UI)
	if strings.Contains(tail, "Tab to amend") {
		score += 2
		matchedIndicators = append(matchedIndicators, "tab_to_amend")
	}

	// 5. Y/N PROMPT
	if regexp.MustCompile(`\(y/n\)\s*$`).MatchString(tail) {
		score += 3
		matchedIndicators = append(matchedIndicators, "yn_prompt")
	}

	// 6. PERMISSION RULE HEADER
	if strings.Contains(tail, "Permission rule") {
		score += 3
		matchedIndicators = append(matchedIndicators, "permission_rule")
	}

	// DEBUG LOGGING
	if debug.Logger != nil && score > 0 {
		debug.Logger.Printf("DETECTION: score=%d, indicators=%v", score, matchedIndicators)
		lastChunk := tail
		if len(lastChunk) > 600 {
			lastChunk = lastChunk[len(lastChunk)-600:]
		}
		debug.Logger.Printf("Tail (last 600):\n%s\n", lastChunk)
	}

	// Score >= 3 required: at least one strong indicator
	return score >= 3, score
}

// NeedsYes checks if prompt needs 'yes' text (vs just Enter)
func NeedsYes(text string) bool {
	clean := StripANSI(text)
	pattern := regexp.MustCompile(`(?i)Type.*yes|Enter.*yes|\(y/n\)`)
	return pattern.MatchString(clean)
}

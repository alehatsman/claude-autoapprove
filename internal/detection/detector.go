package detection

import (
	"regexp"
	"strings"

	"github.com/alehatsman/claude-autoapprove/internal/debug"
)

// ANSI escape code patterns
var ansiCursorPattern = regexp.MustCompile(`\x1b\[[\d;]*[ABCDEFGHJKfsu]`) // Cursor movements - replace with space
var ansiEscapePattern = regexp.MustCompile(`\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~]|\][^\x07]*\x07)`) // All ANSI codes
var controlChars = regexp.MustCompile(`[\x00-\x08\x0B-\x0C\x0E-\x1F]`) // Control chars except \t, \n, \r

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

// IsPrompt detects if text is a permission prompt using UI element detection
// This focuses on the UI chrome that Claude Code controls, not Claude's responses
// Returns (isPrompt bool, score int)
func IsPrompt(text string) (bool, int) {
	clean := StripANSI(text)
	score := 0
	matchedIndicators := []string{}

	// SAFETY FIRST: Reject if inside code block or if most content is code
	backtickCount := strings.Count(clean, "```")

	// Odd number = inside unclosed code block
	if backtickCount%2 == 1 {
		return false, 0
	}

	// Even number but > 0 = closed code blocks exist
	// Check if potential prompt is actually inside the code blocks
	if backtickCount >= 2 {
		// Find the last occurrence of ```
		lastBacktickPos := strings.LastIndex(clean, "```")
		// If UI elements appear before the last ```, they're likely inside code
		enterPos := strings.Index(clean, "Enter to approve")
		if enterPos > 0 && enterPos < lastBacktickPos {
			return false, 0
		}
		yesPos := strings.Index(clean, "1. Yes")
		if yesPos > 0 && yesPos < lastBacktickPos {
			return false, 0
		}
	}

	// Reject if this looks like a comment or documentation
	// Comments usually have // or # or * at the start of lines
	lines := strings.Split(clean, "\n")
	commentLineCount := 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") ||
		   strings.HasPrefix(trimmed, "#") ||
		   strings.HasPrefix(trimmed, "*") {
			commentLineCount++
		}
	}
	// If more than 50% of lines are comments, probably not a real prompt
	if len(lines) > 0 && commentLineCount > len(lines)/2 {
		return false, 0
	}

	// UI CHROME DETECTION (controlled by Claude Code, not Claude's responses)
	// These patterns should be 100% consistent

	// 1. YES/NO BUTTONS (strongest indicator - Claude Code's button UI)
	hasYesButton := strings.Contains(clean, "1. Yes") ||
		strings.Contains(clean, "1) Yes") ||
		strings.Contains(clean, "• Yes")

	yesNoPattern := regexp.MustCompile(`[23][\.\)]\s*No|•\s*No`)
	hasNoButton := yesNoPattern.MatchString(clean)

	if hasYesButton && hasNoButton {
		score += 5
		matchedIndicators = append(matchedIndicators, "yes_no_buttons")
	}

	// 2. ENTER TO APPROVE (strong indicator - primary action UI)
	hasEnterApprove := strings.Contains(clean, "Enter to approve") ||
		strings.Contains(clean, "Enter to confirm")
	if hasEnterApprove {
		score += 3
		matchedIndicators = append(matchedIndicators, "enter_to_approve")
	}

	// 3. ESC TO CANCEL (medium indicator - secondary action UI)
	if strings.Contains(clean, "Esc to cancel") {
		score += 2
		matchedIndicators = append(matchedIndicators, "esc_to_cancel")
	}

	// 4. TAB TO AMEND (medium indicator - tertiary action UI)
	if strings.Contains(clean, "Tab to amend") {
		score += 2
		matchedIndicators = append(matchedIndicators, "tab_to_amend")
	}

	// 5. Y/N PROMPT (strong indicator - simple prompt format)
	ynPattern := regexp.MustCompile(`\(y/n\)\s*$`)
	if ynPattern.MatchString(clean) {
		score += 3
		matchedIndicators = append(matchedIndicators, "yn_prompt")
	}

	// 6. PERMISSION RULE HEADER (strong indicator - official prompt identifier)
	if strings.Contains(clean, "Permission rule") {
		score += 3
		matchedIndicators = append(matchedIndicators, "permission_rule")
	}

	// 7. ADDITIONAL CONTEXT (optional - adds confidence if present)
	// These are less reliable but can add context
	if strings.Contains(clean, "Do you want to proceed?") {
		score++
		matchedIndicators = append(matchedIndicators, "proceed_phrase")
	}
	if strings.Contains(clean, "Would you like to proceed?") {
		score++
		matchedIndicators = append(matchedIndicators, "would_like_phrase")
	}

	// DEBUG LOGGING
	shouldLog := debug.Logger != nil && (score > 0 || len(matchedIndicators) > 0)
	if shouldLog {
		lastChunk := clean
		if len(clean) > 600 {
			lastChunk = clean[len(clean)-600:]
		}
		debug.Logger.Printf("DETECTION: score=%d, indicators=%v, backticks=%d", score, matchedIndicators, backtickCount)
		if score > 0 {
			debug.Logger.Printf("Buffer tail (last 600):\n%s\n", lastChunk)
		}
	}

	// THRESHOLD: Need score >= 3 to trigger
	// This requires at least one of:
	// - "Yes/No buttons" (5) [STRONGEST]
	// - "Enter to approve" (3)
	// - "Permission rule" (3)
	// - "y/n" prompt (3)
	// - "Esc to cancel" (2) + "Tab to amend" (2)
	detected := score >= 3

	return detected, score
}

// NeedsYes checks if prompt needs 'yes' text (vs just Enter)
func NeedsYes(text string) bool {
	clean := StripANSI(text)
	pattern := regexp.MustCompile(`(?i)Type.*yes|Enter.*yes|\(y/n\)`)
	return pattern.MatchString(clean)
}

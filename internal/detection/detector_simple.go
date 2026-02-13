package detection

import (
	"regexp"
	"strings"

	"github.com/alehatsman/claude-autoapprove/internal/debug"
)

// IsPromptSimple uses a simplified approach: detect only UI elements that Claude Code controls
// This is more robust than trying to guess what Claude might say
func IsPromptSimple(text string) (bool, int) {
	clean := StripANSI(text)
	score := 0
	matchedIndicators := []string{}

	// UI CHROME DETECTION (controlled by Claude Code, not Claude's responses)
	// These patterns should be 100% consistent

	// 1. APPROVAL BUTTONS (strongest indicator)
	hasYesButton := strings.Contains(clean, "1. Yes") ||
	                strings.Contains(clean, "1) Yes") ||
	                strings.Contains(clean, "• Yes")

	noButtonPattern := regexp.MustCompile(`[23][\.\)]\s*No|•\s*No`)
	hasNoButton := noButtonPattern.MatchString(clean)

	if hasYesButton && hasNoButton {
		score += 5  // Very strong indicator
		matchedIndicators = append(matchedIndicators, "yes_no_buttons")
	}

	// 2. UI INSTRUCTION ELEMENTS
	hasEnterApprove := strings.Contains(clean, "Enter to approve") ||
	                   strings.Contains(clean, "Enter to confirm")
	if hasEnterApprove {
		score += 3  // Strong indicator
		matchedIndicators = append(matchedIndicators, "enter_to_approve")
	}

	hasEscCancel := strings.Contains(clean, "Esc to cancel")
	if hasEscCancel {
		score += 2  // Medium indicator
		matchedIndicators = append(matchedIndicators, "esc_to_cancel")
	}

	hasTabAmend := strings.Contains(clean, "Tab to amend")
	if hasTabAmend {
		score += 2  // Medium indicator
		matchedIndicators = append(matchedIndicators, "tab_to_amend")
	}

	// 3. Y/N PROMPT (simple prompts)
	ynPattern := regexp.MustCompile(`\(y/n\)\s*$`)
	if ynPattern.MatchString(clean) {
		score += 3
		matchedIndicators = append(matchedIndicators, "yn_prompt")
	}

	// 4. "Permission rule" header (official prompt identifier)
	if strings.Contains(clean, "Permission rule") {
		score += 3
		matchedIndicators = append(matchedIndicators, "permission_rule")
	}

	// SAFETY: Ignore if inside unclosed code block
	backtickCount := strings.Count(clean, "```")
	if backtickCount%2 == 1 {
		score = 0
		matchedIndicators = append(matchedIndicators, "inside_code_block")
	}

	// QUALITY CHECK: Require at least a question mark or colon (indicates actual prompt)
	hasQuestionOrPrompt := strings.Contains(clean, "?") ||
	                       strings.Contains(clean, ":") ||
	                       strings.Contains(clean, "Permission rule")
	if !hasQuestionOrPrompt && score > 0 {
		score = score / 2  // Reduce confidence if no question/prompt indicator
	}

	// DEBUG LOGGING
	if debug.Logger != nil && score > 0 {
		lastChunk := clean
		if len(clean) > 600 {
			lastChunk = clean[len(clean)-600:]
		}
		debug.Logger.Printf("DETECTION: score=%d, indicators=%v", score, matchedIndicators)
		debug.Logger.Printf("Buffer tail (last 600):\n%s\n", lastChunk)
	}

	// THRESHOLD: Need score >= 3 to trigger
	// This requires at least:
	// - "Enter to approve" (3) OR
	// - "Yes/No buttons" (5) OR
	// - "Permission rule" (3) OR
	// - "y/n" (3) OR
	// - "Esc to cancel" (2) + "Tab to amend" (2)
	detected := score >= 3

	return detected, score
}

// IsPromptUltraSimple is the most conservative detector - only triggers on clear UI elements
// Use this if you want to minimize false positives at the cost of some false negatives
func IsPromptUltraSimple(text string) (bool, int) {
	clean := StripANSI(text)

	// Only detect if we see BOTH:
	// 1. Yes/No buttons OR "Enter to approve"
	// 2. At least one other UI element

	hasYesNo := (strings.Contains(clean, "1. Yes") || strings.Contains(clean, "1) Yes")) &&
	            (strings.Contains(clean, "2. No") || strings.Contains(clean, "3. No") ||
	             strings.Contains(clean, "2) No") || strings.Contains(clean, "3) No"))

	hasEnterApprove := strings.Contains(clean, "Enter to approve") ||
	                   strings.Contains(clean, "Enter to confirm")

	hasEscCancel := strings.Contains(clean, "Esc to cancel")
	hasPermissionRule := strings.Contains(clean, "Permission rule")

	// Simple logic: need strong indicator + at least one other element
	detected := (hasYesNo || hasEnterApprove) && (hasEscCancel || hasPermissionRule || hasYesNo || hasEnterApprove)

	// Safety: not in code block
	backtickCount := strings.Count(clean, "```")
	if backtickCount%2 == 1 {
		detected = false
	}

	score := 0
	if detected {
		score = 5
	}

	return detected, score
}

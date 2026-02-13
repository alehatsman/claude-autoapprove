package detection

import (
	"strings"
	"testing"
)

// Test cases based on REAL Claude Code permission prompts
func TestIsPrompt_RealPromptExamples(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
		minScore int
	}{
		{
			name: "Standard file creation prompt",
			input: `Do you want to create a new file?

1. Yes
2. No

Esc to cancel | Enter to approve`,
			expected: true,
			minScore: 5,
		},
		{
			name: "Permission rule prompt",
			input: `Permission rule

Do you want to proceed with this action?

1. Yes
2. No

Enter to approve | Esc to cancel`,
			expected: true,
			minScore: 6,
		},
		{
			name: "Simple y/n prompt",
			input: `Type 'yes' to continue (y/n)`,
			expected: true,
			minScore: 3,
		},
		{
			name: "File edit with Tab option",
			input: `Do you want to edit main.go?

1. Yes
2. No
3. View details

Tab to amend | Esc to cancel | Enter to approve`,
			expected: true,
			minScore: 7,
		},
		{
			name: "Bash command permission",
			input: `Allow running bash command?

Enter to confirm | Esc to cancel`,
			expected: true,
			minScore: 3,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected, score := IsPrompt(tt.input)
			if detected != tt.expected {
				t.Errorf("Expected detected=%v, got %v (score=%d)", tt.expected, detected, score)
			}
			if detected && score < tt.minScore {
				t.Errorf("Expected score >= %d, got %d", tt.minScore, score)
			}
		})
	}
}

// Test that we DON'T detect false positives
func TestIsPrompt_FalsePositives(t *testing.T) {
	tests := []struct {
		name  string
		input string
	}{
		{
			name: "Code example with mock prompt",
			input: "```\nDo you want to proceed?\n1. Yes\n2. No\n```",
		},
		{
			name: "Regular conversation",
			input: "Here's how to implement the feature. Do you want me to explain more?",
		},
		{
			name: "Documentation text",
			input: `The system will ask "Do you want to proceed?" with options Yes/No.`,
		},
		{
			name: "Code comment about prompts",
			input: `// This function shows a prompt: "Enter to approve"`,
		},
		{
			name: "Incomplete prompt (no UI elements)",
			input: `Do you want to create a file? Yes or No?`,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected, score := IsPrompt(tt.input)
			if detected {
				t.Errorf("False positive: detected=%v, score=%d for input:\n%s", detected, score, tt.input)
			}
		})
	}
}

// Test ANSI stripping
func TestStripANSI(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected string
	}{
		{
			name:     "Color codes",
			input:    "\x1b[32mGreen text\x1b[0m",
			expected: "Green text",
		},
		{
			name:     "Cursor movements replaced with spaces",
			input:    "Text\x1b[2AMore text",
			expected: "Text More text",
		},
		{
			name:     "Multiple ANSI sequences",
			input:    "\x1b[1;31mBold Red\x1b[0m\x1b[2J\x1b[H",
			expected: "Bold Red ",
		},
		{
			name:     "Carriage returns normalized",
			input:    "Line 1\rLine 2",
			expected: "Line 1 Line 2",
		},
		{
			name:     "Multiple spaces collapsed",
			input:    "Text    with     spaces",
			expected: "Text with spaces",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := StripANSI(tt.input)
			// Normalize whitespace for comparison
			result = strings.TrimSpace(result)
			expected := strings.TrimSpace(tt.expected)
			if result != expected {
				t.Errorf("Expected '%s', got '%s'", expected, result)
			}
		})
	}
}

// Test NeedsYes detection
func TestNeedsYes(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name:     "Explicit type yes",
			input:    "Type 'yes' to continue",
			expected: true,
		},
		{
			name:     "Enter yes pattern",
			input:    "Enter yes to confirm",
			expected: true,
		},
		{
			name:     "y/n shorthand",
			input:    "Proceed? (y/n)",
			expected: true,
		},
		{
			name:     "Just Enter needed",
			input:    "Enter to approve",
			expected: false,
		},
		{
			name:     "Button-based prompt",
			input:    "1. Yes\n2. No\nEnter to approve",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			result := NeedsYes(tt.input)
			if result != tt.expected {
				t.Errorf("Expected %v, got %v for input: %s", tt.expected, result, tt.input)
			}
		})
	}
}

// Test code block detection safety
func TestIsPrompt_CodeBlockSafety(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		detected bool
	}{
		{
			name: "Inside code block (odd backticks)",
			input: "```\nDo you want to proceed?\n1. Yes\n2. No\nEnter to approve",
			detected: false,
		},
		{
			name: "Outside code block (even backticks)",
			input: "```\ncode here\n```\n\nDo you want to proceed?\n1. Yes\n2. No\nEnter to approve",
			detected: true,
		},
		{
			name: "Multiple closed code blocks",
			input: "```\nblock1\n```\n```\nblock2\n```\n\nDo you want to proceed?\n1. Yes\n2. No\nEnter to approve",
			detected: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected, _ := IsPrompt(tt.input)
			if detected != tt.detected {
				t.Errorf("Expected detected=%v, got %v", tt.detected, detected)
			}
		})
	}
}

// Benchmark detection performance
func BenchmarkIsPrompt(b *testing.B) {
	prompt := `Permission rule

Do you want to create a new file main.go?

1. Yes
2. No
3. View details

Tab to amend | Esc to cancel | Enter to approve`

	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		IsPrompt(prompt)
	}
}

func BenchmarkStripANSI(b *testing.B) {
	text := "\x1b[32m\x1b[1mColored\x1b[0m text with \x1b[2J\x1b[H many \x1b[31mANSI codes\x1b[0m"
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		StripANSI(text)
	}
}

// Test simplified detector
func TestIsPromptSimple(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name: "Clear Yes/No buttons",
			input: `Do you want to proceed?

1. Yes
2. No

Enter to approve`,
			expected: true,
		},
		{
			name: "Enter to approve only",
			input: `Allow this action?

Enter to approve | Esc to cancel`,
			expected: true,
		},
		{
			name: "Permission rule header",
			input: `Permission rule

Proceed with changes?

1. Yes
2. No`,
			expected: true,
		},
		{
			name: "No UI elements (should not detect)",
			input: `This is just regular text about yes and no options.`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected, score := IsPromptSimple(tt.input)
			if detected != tt.expected {
				t.Errorf("Expected detected=%v, got %v (score=%d)", tt.expected, detected, score)
			}
		})
	}
}

// Test ultra-simple detector
func TestIsPromptUltraSimple(t *testing.T) {
	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		{
			name: "Strong indicators present",
			input: `Do you want to proceed?

1. Yes
2. No

Esc to cancel | Enter to approve`,
			expected: true,
		},
		{
			name: "Only Enter to approve + Esc",
			input: `Confirm action?

Enter to approve | Esc to cancel`,
			expected: true,
		},
		{
			name: "Weak indicators only",
			input: `This mentions yes and no but has no UI elements`,
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			detected, _ := IsPromptUltraSimple(tt.input)
			if detected != tt.expected {
				t.Errorf("Expected detected=%v, got %v", tt.expected, detected)
			}
		})
	}
}

// Test with real buffer content (partial prompts, ANSI codes)
func TestIsPrompt_RealWorldBuffer(t *testing.T) {
	// Simulate real PTY output with ANSI codes and partial content
	realBuffer := "\x1b[2J\x1b[H\x1b[32mClaude:\x1b[0m I'll help you create that file.\n\n" +
	              "Permission rule\n\n" +
	              "Do you want to \x1b[1mcreate\x1b[0m the file \x1b[33mmain.go\x1b[0m?\n\n" +
	              "\x1b[36m1. Yes\x1b[0m\n" +
	              "\x1b[36m2. No\x1b[0m\n\n" +
	              "\x1b[90mEsc to cancel\x1b[0m | \x1b[32mEnter to approve\x1b[0m"

	detected, score := IsPrompt(realBuffer)
	if !detected {
		t.Errorf("Failed to detect real-world prompt (score=%d)", score)
	}
	if score < 6 {
		t.Errorf("Expected high confidence score >= 6, got %d", score)
	}
}

package terminal

import (
	"fmt"
	"os"
	"strings"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// Terminal manages terminal dimensions and rendering
type Terminal struct {
	Height       int
	Width        int
	ContentRows  int
	StatusBarRow int
	ptmx         *os.File
}

// New creates a new Terminal instance
func New(ptmx *os.File) *Terminal {
	return &Terminal{
		Height:       24,
		Width:        80,
		ContentRows:  22,
		StatusBarRow: 23,
		ptmx:         ptmx,
	}
}

// UpdateSize updates terminal dimensions and sets scrolling region
func (t *Terminal) UpdateSize() error {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return err
	}

	t.Height = height
	t.Width = width
	t.ContentRows = max(1, height-2)
	t.StatusBarRow = t.ContentRows + 1

	// Set PTY size
	winsize := &pty.Winsize{
		Rows: uint16(height),
		Cols: uint16(width),
	}
	if err := pty.Setsize(t.ptmx, winsize); err != nil {
		return err
	}

	// Set scrolling region
	fmt.Fprintf(os.Stdout, "\033[1;%dr", t.ContentRows)
	return nil
}

// DrawStatus draws a message in the status bar with the given color
func (t *Terminal) DrawStatus(message, color string) {
	// Save cursor
	fmt.Fprint(os.Stderr, "\0337")

	// Position cursor at border line
	fmt.Fprintf(os.Stderr, "\033[%d;1H", t.StatusBarRow-1)

	// Clear status area (border + message + empty line)
	for row := t.StatusBarRow - 1; row <= t.Height; row++ {
		fmt.Fprintf(os.Stderr, "\033[%d;1H\033[K", row)
	}

	// Draw top border
	border := strings.Repeat("─", t.Width)
	fmt.Fprintf(os.Stderr, "\033[%d;1H\033[2m%s\033[0m", t.StatusBarRow-1, border)

	// Draw message
	fmt.Fprintf(os.Stderr, "\033[%d;1H\033[%sm%s\033[0m", t.StatusBarRow, color, message)

	// Draw bottom border
	fmt.Fprintf(os.Stderr, "\033[%d;1H\033[2m%s\033[0m", t.StatusBarRow+1, border)

	// Restore cursor
	fmt.Fprint(os.Stderr, "\0338")
}

// ClearStatus shows ready state in status bar
func (t *Terminal) ClearStatus(autoApprove bool, approvalCount int, delaySeconds int) {
	if autoApprove {
		msg := fmt.Sprintf("Ready (auto-approve ON, %ds delay) [Ctrl+A=toggle, Ctrl+↑↓=delay]", delaySeconds)
		if approvalCount > 0 {
			msg = fmt.Sprintf("Ready (auto-approve ON, %d executed, %ds delay) [Ctrl+A=toggle, Ctrl+↑↓=delay]", approvalCount, delaySeconds)
		}
		t.DrawStatus(msg, "2")
	} else {
		t.DrawStatus(fmt.Sprintf("Ready (auto-approve OFF, %ds delay) [Ctrl+A=toggle, Ctrl+↑↓=delay]", delaySeconds), "90")
	}
}

// ResetScrolling resets the terminal scrolling region
func (t *Terminal) ResetScrolling() {
	fmt.Fprint(os.Stdout, "\033[r")
}

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

package main

import (
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

const maxBuffer = 10_000

// Config holds configuration for the wrapper.
type Config struct {
	CountdownSeconds int
}

// ── Terminal ──────────────────────────────────────────────────────────────────

type Terminal struct {
	Height int
	Width  int
	ptmx   *os.File
}

func newTerminal(ptmx *os.File) *Terminal {
	return &Terminal{Height: 24, Width: 80, ptmx: ptmx}
}

func (t *Terminal) UpdateSize() error {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return err
	}
	t.Height = height
	t.Width = width
	return pty.Setsize(t.ptmx, &pty.Winsize{Rows: uint16(height), Cols: uint16(width)})
}

// ForceRedraw triggers a full UI redraw in the child process by briefly
// changing the PTY width by one column and then restoring it. Some TUI
// frameworks (e.g. Ink) skip redraws when dimensions are unchanged, so a
// plain UpdateSize() after approval may do nothing. The toggle guarantees
// an actual size change and therefore a complete repaint.
func (t *Terminal) ForceRedraw() {
	if t.Width < 2 {
		return
	}
	pty.Setsize(t.ptmx, &pty.Winsize{Rows: uint16(t.Height), Cols: uint16(t.Width - 1)})
	pty.Setsize(t.ptmx, &pty.Winsize{Rows: uint16(t.Height), Cols: uint16(t.Width)})
}

func (t *Terminal) DrawStatus(message, color string) {
	row := t.Height - 1
	if row < 1 {
		row = 1
	}
	fmt.Fprint(os.Stderr, "\0337")
	fmt.Fprintf(os.Stderr, "\033[%d;1H\033[K\033[%sm%s\033[0m", row, color, message)
	fmt.Fprint(os.Stderr, "\0338")
}

// ── ClaudeWrapper ─────────────────────────────────────────────────────────────

type ClaudeWrapper struct {
	// Config — set once before Run()
	autoApprove      bool
	countdownSeconds int

	// Process / PTY / terminal
	ptmx     *os.File
	cmd      *exec.Cmd
	oldState *term.State
	term     *Terminal

	// Output buffer
	buffer        string
	approvalCount int

	// Countdown state
	countdownActive  bool
	countdownEnd     time.Time
	countdownStart   int // buffer length when countdown began

	// Transient status flash
	statusMsg      string
	statusMsgColor string
	statusMsgUntil time.Time

	// Rescue / periodic redraw tracking
	lastOutputTime  time.Time
	lastRescueTime  time.Time
	lastRedrawTime  time.Time
}

func New() *ClaudeWrapper {
	return NewWithConfig(nil)
}

func NewWithConfig(cfg *Config) *ClaudeWrapper {
	if cfg == nil {
		cfg = &Config{CountdownSeconds: 1}
	}
	if cfg.CountdownSeconds < 0 {
		cfg.CountdownSeconds = 1
	}
	return &ClaudeWrapper{
		autoApprove:      true,
		countdownSeconds: cfg.CountdownSeconds,
	}
}

// ── Cleanup ───────────────────────────────────────────────────────────────────

func (w *ClaudeWrapper) cleanup() {
	defer func() { recover() }()
	if w.oldState != nil {
		term.Restore(int(os.Stdin.Fd()), w.oldState)
	}
	if w.ptmx != nil {
		w.ptmx.Close()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		w.cmd.Process.Kill()
	}
	closeDebug()
}

// ── Approval ──────────────────────────────────────────────────────────────────

func (w *ClaudeWrapper) startCountdown() {
	w.countdownActive = true
	w.countdownEnd = time.Now().Add(time.Duration(w.countdownSeconds) * time.Second)
	w.countdownStart = len(w.buffer)
	logf(">>> COUNTDOWN START: end=%v start=%d", w.countdownEnd, w.countdownStart)
}

func (w *ClaudeWrapper) executeApproval() {
	w.countdownActive = false
	w.approvalCount++

	needsYes := NeedsYes(w.buffer)

	// Keep only content that arrived after the countdown started — it may be a
	// second prompt that was blocked while countdownActive was true. Discard
	// everything before to prevent stale re-detection of what we just approved.
	start := w.countdownStart
	if start > len(w.buffer) {
		start = len(w.buffer)
	}
	w.buffer = w.buffer[start:]

	logf(">>> APPROVAL #%d needsYes=%v remainingBuf=%d", w.approvalCount, needsYes, len(w.buffer))

	input := []byte("\r")
	if needsYes {
		input = []byte("yes\r")
	}
	if _, err := w.ptmx.Write(input); err != nil {
		logf("ptmx write error: %v", err)
		w.flash("✗ Failed to send approval", "31", time.Second)
		return
	}

	logf(">>> APPROVAL SENT")
	w.flash(fmt.Sprintf("✓ Auto-approved (#%d)", w.approvalCount), "32", 800*time.Millisecond)

	// Force Claude to fully redraw — surfaces any immediately-following dialog
	// and clears any display artifacts left by the approval interaction.
	if w.term != nil {
		w.term.ForceRedraw()
	}
}

func (w *ClaudeWrapper) flash(msg, color string, d time.Duration) {
	w.statusMsg = msg
	w.statusMsgColor = color
	w.statusMsgUntil = time.Now().Add(d)
	w.drawStatus()
}

// ── Event handlers ────────────────────────────────────────────────────────────

func (w *ClaudeWrapper) handleOutput(data []byte) {
	os.Stdout.Write(data)
	w.lastOutputTime = time.Now()

	w.buffer += string(data)
	if len(w.buffer) > maxBuffer {
		w.buffer = w.buffer[len(w.buffer)-maxBuffer:]
	}

	logf("handleOutput: bufLen=%d countdownActive=%v", len(w.buffer), w.countdownActive)

	if w.autoApprove && !w.countdownActive {
		if ok, score := IsPrompt(w.buffer); ok {
			logf("IsPrompt=true score=%d → startCountdown", score)
			w.startCountdown()
			if w.countdownSeconds == 0 {
				w.executeApproval()
			}
		}
	}
}

func (w *ClaudeWrapper) handleInput(data []byte) {
	if len(data) == 0 {
		return
	}

	if len(data) == 1 && data[0] == 0x01 { // Ctrl+A — toggle
		w.toggleAutoApprove()
		return
	}

	if !w.countdownActive {
		if len(data) >= 6 && string(data[:6]) == "\x1b[1;5A" { // Ctrl+Up
			w.changeDelay(true)
			return
		}
		if len(data) >= 6 && string(data[:6]) == "\x1b[1;5B" { // Ctrl+Down
			w.changeDelay(false)
			return
		}
	}

	if w.countdownActive {
		if data[0] == '\r' || data[0] == '\n' {
			w.executeApproval()
		} else {
			w.countdownActive = false
			w.flash("✗ Cancelled", "90", 500*time.Millisecond)
		}
		return
	}

	if _, err := w.ptmx.Write(data); err != nil {
		logf("failed to forward user input: %v", err)
	}
}

// checkBuffer runs on every status tick (200ms). Handles two cases:
//  1. Prompt sitting in buffer with no active countdown — start/execute.
//  2. Claude idle with empty buffer — force a redraw so any pending dialog
//     re-flows through handleOutput (rescues prompts lost while countdownActive).
func (w *ClaudeWrapper) checkBuffer() {
	if !w.autoApprove || w.countdownActive {
		return
	}

	if w.buffer != "" {
		if ok, _ := IsPrompt(w.buffer); ok {
			logf(">>> WATCHDOG: missed prompt detected")
			w.startCountdown()
			if w.countdownSeconds == 0 {
				w.executeApproval()
			}
			return
		}
	}

	if time.Since(w.lastOutputTime) >= 2*time.Second && time.Since(w.lastRescueTime) >= 3*time.Second {
		logf(">>> WATCHDOG: idle rescue — forcing redraw")
		w.lastRescueTime = time.Now()
		if w.term != nil {
			w.term.ForceRedraw()
		}
	}
}

func (w *ClaudeWrapper) drawStatus() {
	if w.term == nil {
		return
	}
	if time.Now().Before(w.statusMsgUntil) {
		w.term.DrawStatus(w.statusMsg, w.statusMsgColor)
		return
	}
	if w.countdownActive {
		secs := int(math.Ceil(time.Until(w.countdownEnd).Seconds()))
		if secs < 0 {
			secs = 0
		}
		w.term.DrawStatus(fmt.Sprintf("⏱  Auto-approving in %ds... (Enter=now, any key=cancel, Ctrl+A=off)", secs), "33")
		return
	}
	msg := fmt.Sprintf("auto-approve ON  %d approved  delay %ds  [Ctrl+A=toggle, Ctrl+↑↓=delay]", w.approvalCount, w.countdownSeconds)
	color := "2"
	if !w.autoApprove {
		msg = fmt.Sprintf("auto-approve OFF  delay %ds  [Ctrl+A=toggle, Ctrl+↑↓=delay]", w.countdownSeconds)
		color = "90"
	}
	w.term.DrawStatus(msg, color)
}

func (w *ClaudeWrapper) toggleAutoApprove() {
	w.countdownActive = false
	w.autoApprove = !w.autoApprove
	if w.autoApprove {
		w.flash("✓ Auto-approve ENABLED", "32", 800*time.Millisecond)
		if w.buffer != "" {
			if ok, _ := IsPrompt(w.buffer); ok {
				w.startCountdown()
			}
		}
	} else {
		w.flash("✗ Auto-approve DISABLED", "31", 800*time.Millisecond)
	}
}

func (w *ClaudeWrapper) changeDelay(increase bool) {
	old := w.countdownSeconds
	if increase && w.countdownSeconds < 60 {
		w.countdownSeconds++
	} else if !increase && w.countdownSeconds > 0 {
		w.countdownSeconds--
	}
	if old != w.countdownSeconds {
		w.flash(fmt.Sprintf("⏱  Delay: %ds → %ds", old, w.countdownSeconds), "36", 800*time.Millisecond)
	}
}

// ── Run ───────────────────────────────────────────────────────────────────────

func (w *ClaudeWrapper) Run(args []string) int {
	defer func() {
		if r := recover(); r != nil {
			w.cleanup()
			fmt.Fprintf(os.Stderr, "\nFatal error: %v\n", r)
			os.Exit(2)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGWINCH)

	var err error
	w.cmd = exec.Command("claude", args...)
	w.ptmx, err = pty.Start(w.cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: failed to start claude: %v\n", err)
		fmt.Fprintf(os.Stderr, "Make sure 'claude' is installed and in your PATH.\n")
		return 1
	}
	defer w.cleanup()

	w.term = newTerminal(w.ptmx)

	w.oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: requires an interactive terminal (TTY): %v\n", err)
		return 1
	}

	w.lastOutputTime = time.Now()
	fmt.Fprint(os.Stdout, "\033[2J\033[H")
	w.term.UpdateSize()
	w.drawStatus()

	stdinChan := make(chan []byte, 10)
	ptmxChan := make(chan []byte, 10)
	errChan := make(chan error, 2)
	done := make(chan struct{})

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := os.Stdin.Read(buf)
			if err != nil {
				if err != io.EOF {
					errChan <- err
				}
				return
			}
			data := make([]byte, n)
			copy(data, buf[:n])
			select {
			case stdinChan <- data:
			case <-done:
				return
			}
		}
	}()

	go func() {
		buf := make([]byte, 4096)
		for {
			n, err := w.ptmx.Read(buf)
			if err != nil {
				if err == io.EOF {
					close(done)
				} else {
					errChan <- err
				}
				return
			}
			data := make([]byte, n)
			copy(data, buf[:n])
			select {
			case ptmxChan <- data:
			case <-done:
				return
			}
		}
	}()

	ticker := time.NewTicker(200 * time.Millisecond)
	defer ticker.Stop()

	for {
		select {
		case data := <-ptmxChan:
			w.handleOutput(data)

		case data := <-stdinChan:
			w.handleInput(data)

		case <-ticker.C:
			if w.countdownActive && !time.Now().Before(w.countdownEnd) {
				w.executeApproval()
			}
			w.checkBuffer()
			if !w.countdownActive && time.Since(w.lastRedrawTime) >= time.Second {
				w.lastRedrawTime = time.Now()
				w.term.ForceRedraw()
			}
			w.drawStatus()

		case sig := <-sigChan:
			if sig == syscall.SIGWINCH {
				w.term.UpdateSize()
				w.drawStatus()
			} else {
				w.cleanup()
				if s, ok := sig.(syscall.Signal); ok {
					os.Exit(128 + int(s))
				}
				os.Exit(1)
			}

		case err := <-errChan:
			fmt.Fprintf(os.Stderr, "\nI/O error: %v\n", err)
			return 1

		case <-done:
			if err := w.cmd.Wait(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					return exitErr.ExitCode()
				}
				return 1
			}
			return 0
		}
	}
}

package wrapper

import (
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/alehatsman/claude-autoapprove/internal/debug"
	"github.com/alehatsman/claude-autoapprove/internal/detection"
	"github.com/alehatsman/claude-autoapprove/internal/terminal"
	"github.com/creack/pty"
	"golang.org/x/term"
)

// Config holds configuration options for the wrapper
type Config struct {
	CountdownSeconds int
	BufferSize       int
}

// ClaudeWrapper manages the Claude process and auto-approval.
//
// All mutable state (buffer, countdown fields, status overlay) is exclusively
// accessed from the main select loop goroutine.  There are no mutexes and no
// countdown goroutine — the countdown is driven by a time.After channel that is
// recomputed at the top of every loop iteration.  This eliminates every race
// condition and deadlock that existed in the previous goroutine-based design.
type ClaudeWrapper struct {
	// Configuration (set once before Run, read-only after)
	autoApprove      bool
	maxBuffer        int
	countdownSeconds int

	// Process / PTY / terminal (set during Run setup, never mutated in loop)
	ptmx     *os.File
	cmd      *exec.Cmd
	oldState *term.State
	term     *terminal.Terminal

	// Rolling output buffer — main select loop goroutine only
	buffer        string
	approvalCount int

	// Countdown state — main select loop goroutine only; no locks needed
	countdownActive        bool
	countdownEnd           time.Time
	bufferAtCountdownStart int

	// Brief status overlay (approved / cancelled / toggle / delay messages)
	statusMsg      string
	statusMsgColor string
	statusMsgUntil time.Time
}

// New creates a new wrapper instance with default config.
func New() *ClaudeWrapper {
	return NewWithConfig(nil)
}

// NewWithConfig creates a new wrapper instance with custom config.
func NewWithConfig(cfg *Config) *ClaudeWrapper {
	if cfg == nil {
		cfg = &Config{CountdownSeconds: 1, BufferSize: 10000}
	}
	if cfg.CountdownSeconds < 0 {
		cfg.CountdownSeconds = 1
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 10000
	}
	return &ClaudeWrapper{
		autoApprove:      true,
		maxBuffer:        cfg.BufferSize,
		countdownSeconds: cfg.CountdownSeconds,
	}
}

// cleanup restores terminal state and cleans up resources.
func (w *ClaudeWrapper) cleanup() {
	if w.term != nil {
		w.term.ResetScrolling()
	}
	if w.oldState != nil {
		term.Restore(int(os.Stdin.Fd()), w.oldState)
	}
	if w.ptmx != nil {
		w.ptmx.Close()
	}
	if w.cmd != nil && w.cmd.Process != nil {
		w.cmd.Process.Kill()
	}
	debug.Close()
}

// emergencyCleanup unconditionally restores the terminal (called from panic recovery).
func (w *ClaudeWrapper) emergencyCleanup() {
	defer func() {
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "\nPanic during emergency cleanup: %v\n", r)
		}
	}()
	fmt.Fprint(os.Stdout, "\033[r")
	if w.oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), w.oldState)
	}
	if w.ptmx != nil {
		_ = w.ptmx.Close()
	}
}

// startCountdown begins a new countdown.  Records the current buffer length as a
// watermark so that post-approval detection only examines genuinely new content.
// Must be called from the main select loop goroutine.
func (w *ClaudeWrapper) startCountdown() {
	w.countdownActive = true
	w.countdownEnd = time.Now().Add(time.Duration(w.countdownSeconds) * time.Second)
	w.bufferAtCountdownStart = len(w.buffer)
	if debug.Logger != nil {
		debug.Logger.Printf(">>> STARTING COUNTDOWN: end=%v watermark=%d <<<",
			w.countdownEnd, w.bufferAtCountdownStart)
	}
}

// executeApproval sends the approval keystroke to Claude and immediately checks
// for a consecutive prompt in the post-watermark buffer content.
// Must be called from the main select loop goroutine.
func (w *ClaudeWrapper) executeApproval() {
	w.countdownActive = false
	w.approvalCount++

	// Snapshot and clear the buffer BEFORE sending the approval keystroke.
	// Clearing first ensures that when handleOutput is next called there is no
	// stale dialog content that could trigger a spurious re-detection.
	accumulated := w.buffer
	watermark := w.bufferAtCountdownStart
	w.buffer = ""

	needsYes := detection.NeedsYes(accumulated)

	if debug.Logger != nil {
		debug.Logger.Printf(">>> EXECUTING APPROVAL #%d: needsYes=%v accumulated=%d watermark=%d <<<",
			w.approvalCount, needsYes, len(accumulated), watermark)
	}

	var writeErr error
	if needsYes {
		_, writeErr = w.ptmx.Write([]byte("yes\r"))
	} else {
		_, writeErr = w.ptmx.Write([]byte("\r"))
	}

	if writeErr != nil {
		if debug.Logger != nil {
			debug.Logger.Printf("ptmx write error during approval: %v", writeErr)
		}
		w.statusMsg = "✗ Failed to send approval"
		w.statusMsgColor = "31"
		w.statusMsgUntil = time.Now().Add(1 * time.Second)
		w.drawStatus()
		return
	}

	if debug.Logger != nil {
		debug.Logger.Printf(">>> APPROVAL SENT <<<")
	}

	// Show brief "approved" overlay; drawStatus will revert to idle once the
	// overlay expires.
	w.statusMsg = fmt.Sprintf("✓ Auto-approved (#%d)", w.approvalCount)
	w.statusMsgColor = "32"
	w.statusMsgUntil = time.Now().Add(800 * time.Millisecond)
	w.drawStatus()

	// Check for a genuinely new prompt in content that arrived DURING the
	// countdown (bytes after the watermark).  Starting the next countdown here
	// means zero latency for back-to-back subagent permission dialogs — the next
	// approval fires in the same main-loop turn as the first.
	if watermark > len(accumulated) {
		watermark = 0 // buffer was trimmed during countdown; check everything
	}
	newContent := accumulated[watermark:]
	if w.autoApprove && len(newContent) > 10 {
		if ok, _ := detection.IsPrompt(newContent); ok {
			if debug.Logger != nil {
				debug.Logger.Printf(">>> CONSECUTIVE PROMPT in post-watermark content (watermark=%d len=%d) <<<",
					watermark, len(newContent))
			}
			w.startCountdown()
		}
	}
}

// handleOutput processes PTY output received from Claude.
// Must be called from the main select loop goroutine.
func (w *ClaudeWrapper) handleOutput(data []byte) {
	os.Stdout.Write(data)

	w.buffer += string(data)
	if len(w.buffer) > w.maxBuffer {
		w.buffer = w.buffer[len(w.buffer)-w.maxBuffer:]
	}

	if debug.Logger != nil {
		debug.Logger.Printf("handleOutput: bufLen=%d countdownActive=%v", len(w.buffer), w.countdownActive)
	}

	if w.autoApprove && !w.countdownActive {
		if ok, score := detection.IsPrompt(w.buffer); ok {
			if debug.Logger != nil {
				debug.Logger.Printf("IsPrompt=true score=%d → startCountdown", score)
			}
			w.startCountdown()
		} else if debug.Logger != nil {
			tail := w.buffer
			if len(tail) > 800 {
				tail = tail[len(tail)-800:]
			}
			debug.Logger.Printf("IsPrompt=false score=%d bufTail:\n%q\n", score, tail)
		}
	}
}

// handleInput processes a keystroke from the user.
// Must be called from the main select loop goroutine.
// Returns false if the main loop should exit (currently never).
func (w *ClaudeWrapper) handleInput(data []byte) bool {
	if len(data) == 0 {
		return true
	}

	// Ctrl+A = toggle auto-approve
	if len(data) == 1 && data[0] == 0x01 {
		w.toggleAutoApprove()
		return true
	}

	// Ctrl+Up = increase delay (ignored during countdown)
	if !w.countdownActive && len(data) >= 6 && string(data[:6]) == "\x1b[1;5A" {
		w.changeDelay(true)
		return true
	}

	// Ctrl+Down = decrease delay (ignored during countdown)
	if !w.countdownActive && len(data) >= 6 && string(data[:6]) == "\x1b[1;5B" {
		w.changeDelay(false)
		return true
	}

	if w.countdownActive {
		// Enter = approve immediately
		if data[0] == '\r' || data[0] == '\n' {
			w.executeApproval()
			return true
		}
		// Any other key = cancel the countdown.
		// The key is NOT forwarded to Claude — it was directed at the wrapper, not
		// at the dialog.  The watchdog will re-detect the dialog within 500 ms if
		// it is still visible.
		w.countdownActive = false
		w.statusMsg = "✗ Cancelled"
		w.statusMsgColor = "90"
		w.statusMsgUntil = time.Now().Add(500 * time.Millisecond)
		w.drawStatus()
		return true
	}

	// Forward to Claude
	if _, err := w.ptmx.Write(data); err != nil {
		if debug.Logger != nil {
			debug.Logger.Printf("Failed to forward user input: %v", err)
		}
	}
	return true
}

// drawStatus renders the status bar according to current state.
// Priority order: brief overlay message > countdown in progress > idle ready.
// Must be called from the main select loop goroutine.
func (w *ClaudeWrapper) drawStatus() {
	if w.term == nil {
		return
	}
	if time.Now().Before(w.statusMsgUntil) {
		w.term.DrawStatus(w.statusMsg, w.statusMsgColor)
		return
	}
	if w.countdownActive {
		remaining := time.Until(w.countdownEnd)
		secs := int(math.Ceil(remaining.Seconds()))
		if secs < 0 {
			secs = 0
		}
		msg := fmt.Sprintf("⏱  Auto-approving in %ds... (Enter=now, any key=cancel, Ctrl+A=off)", secs)
		w.term.DrawStatus(msg, "33")
		return
	}
	w.term.ClearStatus(w.autoApprove, w.approvalCount, w.countdownSeconds)
}

// checkBuffer is the periodic watchdog: detects any prompt that handleOutput may
// have missed because a countdown was active when the bytes arrived and they were
// not captured in the post-watermark content of the previous approval.
// Must be called from the main select loop goroutine.
func (w *ClaudeWrapper) checkBuffer() {
	if w.autoApprove && !w.countdownActive && w.buffer != "" {
		if ok, _ := detection.IsPrompt(w.buffer); ok {
			if debug.Logger != nil {
				debug.Logger.Printf(">>> WATCHDOG: missed prompt detected in buffer <<<")
			}
			w.startCountdown()
		}
	}
}

// toggleAutoApprove toggles the auto-approve feature on or off.
// An active countdown is cancelled when toggling off.
// Must be called from the main select loop goroutine.
func (w *ClaudeWrapper) toggleAutoApprove() {
	if w.countdownActive {
		w.countdownActive = false
	}
	w.autoApprove = !w.autoApprove

	if w.autoApprove {
		w.statusMsg = "✓ Auto-approve ENABLED"
		w.statusMsgColor = "32"
	} else {
		w.statusMsg = "✗ Auto-approve DISABLED"
		w.statusMsgColor = "31"
	}
	w.statusMsgUntil = time.Now().Add(800 * time.Millisecond)
	w.drawStatus()

	// If re-enabled and a dialog is already in the buffer, start countdown now.
	if w.autoApprove && w.buffer != "" {
		if ok, _ := detection.IsPrompt(w.buffer); ok {
			w.startCountdown()
		}
	}
}

// changeDelay increases or decreases the countdown delay by one second.
// Must be called from the main select loop goroutine.
func (w *ClaudeWrapper) changeDelay(increase bool) {
	old := w.countdownSeconds
	if increase {
		if w.countdownSeconds < 60 {
			w.countdownSeconds++
		}
	} else {
		if w.countdownSeconds > 0 {
			w.countdownSeconds--
		}
	}
	if old != w.countdownSeconds {
		w.statusMsg = fmt.Sprintf("⏱  Delay: %ds → %ds", old, w.countdownSeconds)
		w.statusMsgColor = "36"
		w.statusMsgUntil = time.Now().Add(800 * time.Millisecond)
		w.drawStatus()
	}
}

// Run starts the wrapper, spawning Claude under a PTY and driving its I/O.
func (w *ClaudeWrapper) Run(args []string) int {
	defer func() {
		if r := recover(); r != nil {
			w.emergencyCleanup()
			fmt.Fprintf(os.Stderr, "\nFatal error: %v\n", r)
			debug.Close()
			os.Exit(2)
		}
	}()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGWINCH)

	cmdArgs := append([]string{}, args...)
	w.cmd = exec.Command("claude", cmdArgs...)

	var err error
	w.ptmx, err = pty.Start(w.cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: Failed to start claude: %v\n", err)
		fmt.Fprintf(os.Stderr, "\nMake sure:\n")
		fmt.Fprintf(os.Stderr, "  - 'claude' command is installed and in your PATH\n")
		fmt.Fprintf(os.Stderr, "  - You have permission to create PTY devices\n")
		return 1
	}
	defer w.cleanup()

	w.term = terminal.New(w.ptmx)

	w.oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Error: claude-autoapprove requires an interactive terminal (TTY)\n")
		fmt.Fprintf(os.Stderr, "Failed to set raw mode: %v\n", err)
		fmt.Fprintf(os.Stderr, "\nPlease run this command directly in your terminal, not:\n")
		fmt.Fprintf(os.Stderr, "  - Through a pipe or redirection\n")
		fmt.Fprintf(os.Stderr, "  - In a non-interactive script\n")
		fmt.Fprintf(os.Stderr, "  - From an automation tool without TTY allocation\n")
		return 1
	}

	fmt.Fprint(os.Stdout, "\033[2J\033[H")
	w.term.UpdateSize()
	w.drawStatus()

	stdinChan := make(chan []byte, 10)
	ptmxChan := make(chan []byte, 10)
	errChan := make(chan error, 2)
	done := make(chan struct{})

	// Goroutine: read from stdin and forward to the main loop.
	go func() {
		buf := make([]byte, 1024)
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

	// Goroutine: read from the PTY and forward to the main loop.
	go func() {
		buf := make([]byte, 1024)
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

	watchdogTicker := time.NewTicker(500 * time.Millisecond)
	defer watchdogTicker.Stop()

	statusTicker := time.NewTicker(200 * time.Millisecond)
	defer statusTicker.Stop()

	// Main event loop — the only goroutine that touches mutable wrapper state.
	for {
		// Recompute the countdown channel on every iteration.  A nil channel in a
		// select case is permanently blocked, so when countdownActive=false the
		// countdown case is effectively disabled at zero cost.
		var countdownCh <-chan time.Time
		if w.countdownActive {
			remaining := time.Until(w.countdownEnd)
			if remaining <= 0 {
				// Deadline already passed (loop was busy); fire synchronously.
				w.executeApproval()
				// executeApproval may start a new countdown; recompute.
				if w.countdownActive {
					remaining = time.Until(w.countdownEnd)
					if remaining > 0 {
						countdownCh = time.After(remaining)
					}
				}
			} else {
				countdownCh = time.After(remaining)
			}
		}

		select {
		case data := <-ptmxChan:
			w.handleOutput(data)

		case data := <-stdinChan:
			w.handleInput(data)

		case <-countdownCh:
			w.executeApproval()

		case <-watchdogTicker.C:
			w.checkBuffer()

		case <-statusTicker.C:
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

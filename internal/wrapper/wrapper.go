package wrapper

import (
	"context"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"sync"
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

// ClaudeWrapper manages the Claude process and auto-approval
type ClaudeWrapper struct {
	autoApprove   bool
	ptmx          *os.File
	cmd           *exec.Cmd
	oldState      *term.State
	buffer        string
	maxBuffer     int
	approvalCount int

	// Configuration
	countdownSeconds int

	// Terminal
	term *terminal.Terminal

	// Countdown state
	countdownRunning    bool
	countdownCancelled  chan struct{}
	countdownApproveNow chan struct{}
	recheckBuffer       chan struct{}
	countdownLock       sync.Mutex
	countdownWg         sync.WaitGroup

	// Thread safety
	bufferLock sync.Mutex
}

// New creates a new wrapper instance with default config
func New() *ClaudeWrapper {
	return NewWithConfig(&Config{
		CountdownSeconds: 3,
		BufferSize:       10000,
	})
}

// NewWithConfig creates a new wrapper instance with custom config
func NewWithConfig(cfg *Config) *ClaudeWrapper {
	if cfg == nil {
		cfg = &Config{
			CountdownSeconds: 3,
			BufferSize:       10000,
		}
	}

	// Apply defaults
	if cfg.CountdownSeconds <= 0 {
		cfg.CountdownSeconds = 3
	}
	if cfg.BufferSize <= 0 {
		cfg.BufferSize = 10000
	}

	return &ClaudeWrapper{
		autoApprove:         true,
		maxBuffer:           cfg.BufferSize,
		countdownSeconds:    cfg.CountdownSeconds,
		countdownCancelled:  make(chan struct{}),
		countdownApproveNow: make(chan struct{}),
		recheckBuffer:       make(chan struct{}, 1),
	}
}

// cleanup restores terminal state and cleans up resources
func (w *ClaudeWrapper) cleanup() {

	// Cancel countdown
	w.countdownLock.Lock()
	if w.countdownRunning {
		close(w.countdownCancelled)
		w.countdownLock.Unlock()
		w.countdownWg.Wait()
	} else {
		w.countdownLock.Unlock()
	}

	// Reset scrolling region
	if w.term != nil {
		w.term.ResetScrolling()
	}

	// Restore terminal
	if w.oldState != nil {
		term.Restore(int(os.Stdin.Fd()), w.oldState)
	}

	// Close PTY
	if w.ptmx != nil {
		w.ptmx.Close()
	}

	// Kill process
	if w.cmd != nil && w.cmd.Process != nil {
		w.cmd.Process.Kill()
	}

	// Close debug logger
	debug.Close()
}

// toggleAutoApprove toggles auto-approve on/off
func (w *ClaudeWrapper) toggleAutoApprove() {
	w.autoApprove = !w.autoApprove

	// Cancel countdown if running and wait for goroutine to finish
	w.countdownLock.Lock()
	if w.countdownRunning {
		close(w.countdownCancelled)
		w.countdownLock.Unlock()
		w.countdownWg.Wait() // Wait for goroutine to finish
		w.countdownLock.Lock()
		w.countdownCancelled = make(chan struct{})
	}
	w.countdownLock.Unlock()

	// Show toggle message
	if w.autoApprove {
		w.term.DrawStatus("✓ Auto-approve ENABLED", "32")
	} else {
		w.term.DrawStatus("✗ Auto-approve DISABLED", "31")
	}

	time.Sleep(800 * time.Millisecond)
	w.term.ClearStatus(w.autoApprove, w.approvalCount)

	// Check buffer for existing prompt if re-enabled
	if w.autoApprove {
		w.bufferLock.Lock()
		bufferCopy := w.buffer
		w.bufferLock.Unlock()

		if bufferCopy != "" {
			detected, _ := detection.IsPrompt(bufferCopy)
			if detected {
				w.startCountdown()
			}
		}
	}
}

// countdownAndApprove handles countdown and approval execution
func (w *ClaudeWrapper) countdownAndApprove(seconds int) {
	defer w.countdownWg.Done()

	for i := seconds; i > 0; i-- {
		// Show countdown message first
		w.term.DrawStatus(fmt.Sprintf("⏱  Auto-approving in %ds... (Enter=now, any key=cancel, Ctrl+A=off)", i), "33")

		select {
		case <-w.countdownApproveNow:
			goto approve
		case <-w.countdownCancelled:
			// Mark countdown as finished immediately
			w.countdownLock.Lock()
			w.countdownRunning = false
			w.countdownLock.Unlock()

			w.term.DrawStatus("✗ Auto-approve cancelled", "90")
			time.Sleep(300 * time.Millisecond)
			w.term.ClearStatus(w.autoApprove, w.approvalCount)

			// Signal main loop to re-check buffer after a short delay
			go func() {
				time.Sleep(500 * time.Millisecond)
				select {
				case w.recheckBuffer <- struct{}{}:
				default:
				}
			}()
			return
		case <-time.After(1 * time.Second):
			// Continue to next iteration
		}
	}

	select {
	case <-w.countdownCancelled:
		// Mark countdown as finished immediately
		w.countdownLock.Lock()
		w.countdownRunning = false
		w.countdownLock.Unlock()

		w.term.DrawStatus("✗ Auto-approve cancelled", "90")
		time.Sleep(300 * time.Millisecond)
		w.term.ClearStatus(w.autoApprove, w.approvalCount)

		// Signal main loop to re-check buffer after a short delay
		go func() {
			time.Sleep(500 * time.Millisecond)
			select {
			case w.recheckBuffer <- struct{}{}:
			default:
			}
		}()
		return
	default:
	}

approve:
	// Execute approval
	w.approvalCount++

	if debug.Logger != nil {
		debug.Logger.Printf(">>> EXECUTING APPROVAL #%d <<<", w.approvalCount)
	}

	select {
	case <-w.countdownApproveNow:
		w.term.DrawStatus(fmt.Sprintf("✓ Approved immediately (#%d)", w.approvalCount), "32")
	default:
		w.term.DrawStatus(fmt.Sprintf("✓ Auto-approved (#%d)", w.approvalCount), "32")
	}

	time.Sleep(300 * time.Millisecond)

	// Send approval (read buffer with lock)
	w.bufferLock.Lock()
	needsYesInput := detection.NeedsYes(w.buffer)
	bufferLen := len(w.buffer)
	w.bufferLock.Unlock()

	if debug.Logger != nil {
		debug.Logger.Printf("Sending approval: needsYes=%v, bufferLen=%d", needsYesInput, bufferLen)
	}

	if needsYesInput {
		n, err := w.ptmx.Write([]byte("yes"))
		if debug.Logger != nil {
			debug.Logger.Printf("Wrote 'yes': %d bytes, err=%v", n, err)
		}
		if err != nil {
			if debug.Logger != nil {
				debug.Logger.Printf("Failed to write 'yes': %v", err)
			}
			// Mark as not running and return early
			w.countdownLock.Lock()
			w.countdownRunning = false
			w.countdownLock.Unlock()
			w.term.DrawStatus("✗ Failed to send approval", "31")
			time.Sleep(1 * time.Second)
			w.term.ClearStatus(w.autoApprove, w.approvalCount)
			return
		}
		time.Sleep(100 * time.Millisecond)
		n, err = w.ptmx.Write([]byte("\r"))
		if debug.Logger != nil {
			debug.Logger.Printf("Wrote Enter: %d bytes, err=%v", n, err)
		}
		if err != nil {
			if debug.Logger != nil {
				debug.Logger.Printf("Failed to write Enter after yes: %v", err)
			}
			w.countdownLock.Lock()
			w.countdownRunning = false
			w.countdownLock.Unlock()
			w.term.DrawStatus("✗ Failed to send approval", "31")
			time.Sleep(1 * time.Second)
			w.term.ClearStatus(w.autoApprove, w.approvalCount)
			return
		}
	} else {
		n, err := w.ptmx.Write([]byte("\r"))
		if debug.Logger != nil {
			debug.Logger.Printf("Wrote Enter: %d bytes, err=%v", n, err)
		}
		if err != nil {
			if debug.Logger != nil {
				debug.Logger.Printf("Failed to write Enter: %v", err)
			}
			w.countdownLock.Lock()
			w.countdownRunning = false
			w.countdownLock.Unlock()
			w.term.DrawStatus("✗ Failed to send approval", "31")
			time.Sleep(1 * time.Second)
			w.term.ClearStatus(w.autoApprove, w.approvalCount)
			return
		}
	}

	if debug.Logger != nil {
		debug.Logger.Printf(">>> APPROVAL SENT <<<")
	}

	// Clear buffer after approval is sent to prevent re-detection
	w.bufferLock.Lock()
	w.buffer = ""
	w.bufferLock.Unlock()

	// Mark countdown as finished BEFORE cleanup so new prompts can be detected
	w.countdownLock.Lock()
	w.countdownRunning = false
	w.countdownLock.Unlock()

	w.term.ClearStatus(w.autoApprove, w.approvalCount)
}

// startCountdown starts the countdown thread
func (w *ClaudeWrapper) startCountdown() {
	w.countdownLock.Lock()
	defer w.countdownLock.Unlock()

	if w.countdownRunning {
		close(w.countdownCancelled)
		w.countdownCancelled = make(chan struct{})
		w.countdownWg.Wait()
	}

	w.countdownRunning = true
	w.countdownApproveNow = make(chan struct{})

	w.countdownWg.Add(1)
	go func() {
		w.countdownAndApprove(w.countdownSeconds)
		// countdownRunning is now set to false inside countdownAndApprove
		// to allow immediate detection of new prompts after approval
	}()
}

// handleUserInput processes input from user
func (w *ClaudeWrapper) handleUserInput(data []byte) bool {
	w.countdownLock.Lock()
	countdownRunning := w.countdownRunning
	w.countdownLock.Unlock()

	// Enter during countdown = approve immediately
	if countdownRunning && (data[0] == '\r' || data[0] == '\n') {
		select {
		case w.countdownApproveNow <- struct{}{}:
		default:
		}
		return true
	}

	// Ctrl+A = toggle
	if len(data) == 1 && data[0] == 0x01 {
		w.toggleAutoApprove()
		return true
	}

	// Any other key during countdown = cancel
	if countdownRunning {
		w.countdownLock.Lock()
		if w.countdownRunning { // Double-check with lock held
			close(w.countdownCancelled)
			w.countdownLock.Unlock()
			w.countdownWg.Wait() // Wait for goroutine
			w.countdownLock.Lock()
			w.countdownCancelled = make(chan struct{})
		}
		w.countdownLock.Unlock()
	}

	// Forward to Claude
	if _, err := w.ptmx.Write(data); err != nil {
		if debug.Logger != nil {
			debug.Logger.Printf("Failed to forward user input: %v", err)
		}
		// Continue even on error - the process might be exiting
	}
	return true
}

// handleClaudeOutput processes output from Claude
func (w *ClaudeWrapper) handleClaudeOutput(data []byte) bool {
	// Write to stdout
	os.Stdout.Write(data)

	// Add to buffer for detection (with lock)
	w.bufferLock.Lock()
	w.buffer += string(data)

	// Keep buffer manageable
	if len(w.buffer) > w.maxBuffer {
		w.buffer = w.buffer[len(w.buffer)-w.maxBuffer:]
	}

	// Make a copy for detection (to minimize lock hold time)
	bufferCopy := w.buffer
	w.bufferLock.Unlock()

	// Check for prompt if auto-approve enabled and not already counting down
	w.countdownLock.Lock()
	countdownRunning := w.countdownRunning
	w.countdownLock.Unlock()

	if debug.Logger != nil {
		debug.Logger.Printf("Checking conditions: autoApprove=%v, countdownRunning=%v", w.autoApprove, countdownRunning)
	}

	if w.autoApprove && !countdownRunning {
		detected, _ := detection.IsPrompt(bufferCopy)
		if detected {
			if debug.Logger != nil {
				debug.Logger.Printf(">>> STARTING COUNTDOWN <<<")
			}
			w.startCountdown()
			// Don't clear buffer here - clear after approval is executed
			// This allows re-detection if countdown is cancelled and auto-approve is re-enabled
			return true
		}
	} else if debug.Logger != nil && !w.autoApprove {
		debug.Logger.Printf("Skipping detection: auto-approve is OFF")
	} else if debug.Logger != nil && countdownRunning {
		debug.Logger.Printf("Skipping detection: countdown already running")
	}

	return false
}

// emergencyCleanup performs minimal cleanup to restore terminal state
// This is called during panic recovery and must not panic itself
func (w *ClaudeWrapper) emergencyCleanup() {
	defer func() {
		// Catch any panics in cleanup itself
		if r := recover(); r != nil {
			fmt.Fprintf(os.Stderr, "\nPanic during emergency cleanup: %v\n", r)
		}
	}()

	// Reset scrolling region (most critical for terminal usability)
	fmt.Fprint(os.Stdout, "\033[r")

	// Restore terminal mode (second most critical)
	if w.oldState != nil {
		_ = term.Restore(int(os.Stdin.Fd()), w.oldState)
	}

	// Close resources
	if w.ptmx != nil {
		_ = w.ptmx.Close()
	}
}

// Run starts the wrapper
func (w *ClaudeWrapper) Run(args []string) int {
	// Panic recovery to ensure terminal is always restored
	defer func() {
		if r := recover(); r != nil {
			w.emergencyCleanup()
			fmt.Fprintf(os.Stderr, "\nFatal error: %v\n", r)
			debug.Close()
			os.Exit(2)
		}
	}()

	// Create context for cancellation
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	// Setup signal handlers
	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGWINCH)

	// Build command
	cmdArgs := append([]string{}, args...)
	w.cmd = exec.Command("claude", cmdArgs...)

	// Create PTY
	var err error
	w.ptmx, err = pty.Start(w.cmd)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to start claude: %v\n", err)
		return 1
	}
	defer w.cleanup()

	// Initialize terminal
	w.term = terminal.New(w.ptmx)

	// Save terminal state and set raw mode
	w.oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set raw mode: %v\n", err)
		return 1
	}

	// Clear screen and setup terminal
	fmt.Fprint(os.Stdout, "\033[2J\033[H")
	w.term.UpdateSize()
	w.term.ClearStatus(w.autoApprove, w.approvalCount)

	// Handle signals in background
	go func() {
		for {
			select {
			case sig := <-sigChan:
				if sig == syscall.SIGWINCH {
					w.term.UpdateSize()
					w.term.ClearStatus(w.autoApprove, w.approvalCount)
				} else {
					w.cleanup()
					if signal, ok := sig.(syscall.Signal); ok {
						os.Exit(128 + int(signal))
					} else {
						os.Exit(1)
					}
				}
			case <-ctx.Done():
				return
			}
		}
	}()

	// Channels for I/O
	stdinChan := make(chan []byte, 10)
	ptmxChan := make(chan []byte, 10)
	errChan := make(chan error, 2)
	done := make(chan struct{})

	// Goroutine to read from stdin
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

	// Goroutine to read from ptmx
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

	// Ticker to periodically refresh the status bar
	statusRefreshTicker := time.NewTicker(500 * time.Millisecond)
	defer statusRefreshTicker.Stop()

	// Main I/O loop
	for {
		select {
		case data := <-stdinChan:
			if !w.handleUserInput(data) {
				return 0
			}

		case data := <-ptmxChan:
			w.handleClaudeOutput(data)

		case <-w.recheckBuffer:
			// Re-check buffer for prompts after countdown cancellation
			w.countdownLock.Lock()
			countdownRunning := w.countdownRunning
			w.countdownLock.Unlock()

			if w.autoApprove && !countdownRunning {
				w.bufferLock.Lock()
				bufferCopy := w.buffer
				w.bufferLock.Unlock()

				if bufferCopy != "" {
					detected, _ := detection.IsPrompt(bufferCopy)
					if detected {
						w.startCountdown()
					}
				}
			}

		case <-statusRefreshTicker.C:
			// Periodically refresh the status bar to keep it visible
			if !w.countdownRunning {
				w.term.ClearStatus(w.autoApprove, w.approvalCount)
			}

		case err := <-errChan:
			fmt.Fprintf(os.Stderr, "\nI/O error: %v\n", err)
			return 1

		case <-done:
			// Wait for process to finish
			if err := w.cmd.Wait(); err != nil {
				if exitErr, ok := err.(*exec.ExitError); ok {
					return exitErr.ExitCode()
				}
				return 1
			}
			return 0

		case <-time.After(50 * time.Millisecond):
			// Periodic check - process might have exited
			if w.cmd.ProcessState != nil {
				return w.cmd.ProcessState.ExitCode()
			}
		}
	}
}

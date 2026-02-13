package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/creack/pty"
	"golang.org/x/term"
)

// ANSI escape code pattern
var ansiPattern = regexp.MustCompile(`\x1b\[[0-9;]*[a-zA-Z]`)

// stripANSI removes ANSI escape codes from text
func stripANSI(text string) string {
	return ansiPattern.ReplaceAllString(text, "")
}

// isPrompt detects if text is a permission prompt
// Returns (isPrompt bool, score int)
func isPrompt(text string) (bool, int) {
	clean := stripANSI(text)
	score := 0

	// Strong indicators
	if strings.Contains(clean, "Permission rule") {
		score += 2
	}
	if strings.Contains(clean, "Do you want to proceed?") {
		score += 2
	}
	if strings.Contains(clean, "Would you like to proceed?") {
		score += 2
	}

	// File operations
	fileOpsPattern := regexp.MustCompile(`(?i)Do you want to (create|edit|delete|modify|write)`)
	if fileOpsPattern.MatchString(clean) {
		score += 2
	}
	wouldLikePattern := regexp.MustCompile(`(?i)Would you like to (create|edit|delete|modify|write)`)
	if wouldLikePattern.MatchString(clean) {
		score += 2
	}

	// UI elements
	if strings.Contains(clean, "Esc to cancel") {
		score++
	}
	if strings.Contains(clean, "Tab to amend") {
		score++
	}
	if strings.Contains(clean, "Enter to approve") || strings.Contains(clean, "Enter to confirm") {
		score++
	}

	// Yes/No buttons (strong indicator)
	hasYes := strings.Contains(clean, "1. Yes") || strings.Contains(clean, "1) Yes")
	yesNoPattern := regexp.MustCompile(`[23]\.\s*No|[23]\)\s*No`)
	hasNo := yesNoPattern.MatchString(clean)
	if hasYes && hasNo {
		score += 3
	}

	// y/n prompt
	ynPattern := regexp.MustCompile(`\(y/n\)\s*$`)
	if ynPattern.MatchString(clean) {
		score++
	}

	// Safety: ignore code blocks
	if strings.Contains(clean, "```") {
		score = 0
	}

	return score >= 3, score
}

// needsYes checks if prompt needs 'yes' text (vs just Enter)
func needsYes(text string) bool {
	clean := stripANSI(text)
	pattern := regexp.MustCompile(`(?i)Type.*yes|Enter.*yes|\(y/n\)`)
	return pattern.MatchString(clean)
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

	// Terminal size
	termHeight     int
	termWidth      int
	contentRows    int
	statusBarRow   int

	// Countdown state
	countdownRunning   bool
	countdownCancelled chan struct{}
	countdownApproveNow chan struct{}
	countdownLock      sync.Mutex
	countdownWg        sync.WaitGroup
}

// NewClaudeWrapper creates a new wrapper instance
func NewClaudeWrapper() *ClaudeWrapper {
	return &ClaudeWrapper{
		autoApprove:         true,
		maxBuffer:           4096,
		termHeight:          24,
		termWidth:           80,
		contentRows:         22,
		statusBarRow:        23,
		countdownCancelled:  make(chan struct{}),
		countdownApproveNow: make(chan struct{}),
	}
}

// updateTerminalSize updates terminal dimensions and sets scrolling region
func (w *ClaudeWrapper) updateTerminalSize() error {
	width, height, err := term.GetSize(int(os.Stdout.Fd()))
	if err != nil {
		return err
	}

	w.termHeight = height
	w.termWidth = width
	w.contentRows = max(1, height-2)
	w.statusBarRow = w.contentRows + 1

	// Set PTY size
	winsize := &pty.Winsize{
		Rows: uint16(height),
		Cols: uint16(width),
	}
	if err := pty.Setsize(w.ptmx, winsize); err != nil {
		return err
	}

	// Set scrolling region
	fmt.Fprintf(os.Stdout, "\033[1;%dr", w.contentRows)
	return nil
}

// drawStatus draws a message in the status bar
func (w *ClaudeWrapper) drawStatus(message, color string) {
	// Save cursor
	fmt.Fprint(os.Stderr, "\0337")

	// Position cursor at status bar
	fmt.Fprintf(os.Stderr, "\033[%d;1H", w.statusBarRow)

	// Clear status area
	for row := w.statusBarRow; row <= w.termHeight; row++ {
		fmt.Fprintf(os.Stderr, "\033[%d;1H\033[K", row)
	}

	// Draw border
	border := strings.Repeat("─", w.termWidth)
	fmt.Fprintf(os.Stderr, "\033[%d;1H\033[2m%s\033[0m", w.statusBarRow, border)

	// Draw message
	fmt.Fprintf(os.Stderr, "\033[%d;1H\033[%sm%s\033[0m", w.statusBarRow+1, color, message)

	// Restore cursor
	fmt.Fprint(os.Stderr, "\0338")
}

// clearStatus shows ready state in status bar
func (w *ClaudeWrapper) clearStatus() {
	if w.autoApprove {
		msg := "Ready (auto-approve ON) [Ctrl+A=toggle]"
		if w.approvalCount > 0 {
			msg = fmt.Sprintf("Ready (auto-approve ON, %d executed) [Ctrl+A=toggle]", w.approvalCount)
		}
		w.drawStatus(msg, "2")
	} else {
		w.drawStatus("Ready (auto-approve OFF) [Ctrl+A=toggle]", "90")
	}
}

// toggleAutoApprove toggles auto-approve on/off
func (w *ClaudeWrapper) toggleAutoApprove() {
	w.autoApprove = !w.autoApprove

	// Cancel countdown if running
	if w.countdownRunning {
		close(w.countdownCancelled)
		w.countdownCancelled = make(chan struct{})
	}

	// Show toggle message
	if w.autoApprove {
		w.drawStatus("✓ Auto-approve ENABLED", "32")
	} else {
		w.drawStatus("✗ Auto-approve DISABLED", "31")
	}

	time.Sleep(800 * time.Millisecond)
	w.clearStatus()

	// Check buffer for existing prompt if re-enabled
	if w.autoApprove && w.buffer != "" {
		detected, _ := isPrompt(w.buffer)
		if detected {
			w.startCountdown()
		}
	}
}

// countdownAndApprove handles countdown and approval execution
func (w *ClaudeWrapper) countdownAndApprove(seconds int) {
	defer w.countdownWg.Done()

	for i := seconds; i > 0; i-- {
		select {
		case <-w.countdownApproveNow:
			goto approve
		case <-w.countdownCancelled:
			w.drawStatus("✗ Auto-approve cancelled", "90")
			time.Sleep(300 * time.Millisecond)
			w.clearStatus()
			return
		case <-time.After(1 * time.Second):
			w.drawStatus(fmt.Sprintf("⏱  Auto-approving in %ds... (Enter=now, any key=cancel, Ctrl+A=off)", i), "33")
		}
	}

	select {
	case <-w.countdownCancelled:
		w.drawStatus("✗ Auto-approve cancelled", "90")
		time.Sleep(300 * time.Millisecond)
		w.clearStatus()
		return
	default:
	}

approve:
	// Execute approval
	w.approvalCount++

	select {
	case <-w.countdownApproveNow:
		w.drawStatus(fmt.Sprintf("✓ Approved immediately (#%d)", w.approvalCount), "32")
	default:
		w.drawStatus(fmt.Sprintf("✓ Auto-approved (#%d)", w.approvalCount), "32")
	}

	time.Sleep(300 * time.Millisecond)

	// Send approval
	if needsYes(w.buffer) {
		w.ptmx.Write([]byte("yes"))
		time.Sleep(100 * time.Millisecond)
		w.ptmx.Write([]byte("\r"))
	} else {
		w.ptmx.Write([]byte("\r"))
	}

	// Clear buffer after approval is sent to prevent re-detection
	w.buffer = ""

	w.clearStatus()
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
		w.countdownAndApprove(3)
		w.countdownLock.Lock()
		w.countdownRunning = false
		w.countdownLock.Unlock()
	}()
}

// cleanup restores terminal state and cleans up resources
func (w *ClaudeWrapper) cleanup() {
	// Cancel countdown
	if w.countdownRunning {
		close(w.countdownCancelled)
		w.countdownWg.Wait()
	}

	// Reset scrolling region
	fmt.Fprint(os.Stdout, "\033[r")

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
}

// handleUserInput processes input from user
func (w *ClaudeWrapper) handleUserInput(data []byte) bool {
	// Enter during countdown = approve immediately
	if w.countdownRunning && (data[0] == '\r' || data[0] == '\n') {
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
	if w.countdownRunning {
		close(w.countdownCancelled)
		w.countdownCancelled = make(chan struct{})
	}

	// Forward to Claude
	w.ptmx.Write(data)
	return true
}

// handleClaudeOutput processes output from Claude
func (w *ClaudeWrapper) handleClaudeOutput(data []byte) bool {
	// Write to stdout
	os.Stdout.Write(data)

	// Add to buffer for detection
	w.buffer += string(data)

	// Keep buffer manageable
	if len(w.buffer) > w.maxBuffer {
		w.buffer = w.buffer[len(w.buffer)-w.maxBuffer:]
	}

	// Check for prompt if auto-approve enabled and not already counting down
	if w.autoApprove && !w.countdownRunning {
		detected, _ := isPrompt(w.buffer)
		if detected {
			w.startCountdown()
			// Don't clear buffer here - clear after approval is executed
			// This allows re-detection if countdown is cancelled and auto-approve is re-enabled
			return true
		}
	}

	return false
}

// run starts the wrapper
func (w *ClaudeWrapper) run(args []string) int {
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

	// Save terminal state and set raw mode
	w.oldState, err = term.MakeRaw(int(os.Stdin.Fd()))
	if err != nil {
		fmt.Fprintf(os.Stderr, "Failed to set raw mode: %v\n", err)
		return 1
	}

	// Clear screen and setup terminal
	fmt.Fprint(os.Stdout, "\033[2J\033[H")
	w.updateTerminalSize()
	w.clearStatus()

	// Handle signals in background
	go func() {
		for sig := range sigChan {
			if sig == syscall.SIGWINCH {
				w.updateTerminalSize()
			} else {
				w.cleanup()
				os.Exit(128 + int(sig.(syscall.Signal)))
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

	// Main I/O loop
	for {
		select {
		case data := <-stdinChan:
			if !w.handleUserInput(data) {
				return 0
			}

		case data := <-ptmxChan:
			w.handleClaudeOutput(data)

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

func max(a, b int) int {
	if a > b {
		return a
	}
	return b
}

func main() {
	wrapper := NewClaudeWrapper()
	exitCode := wrapper.run(os.Args[1:])
	os.Exit(exitCode)
}

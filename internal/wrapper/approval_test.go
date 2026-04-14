// Package wrapper — approval logic tests.
//
// These tests exercise the countdown / approval state machine without a real
// PTY or Claude process.  A pipe replaces w.ptmx so every byte written by
// executeApproval is observable.  A testLoop goroutine mirrors the main select
// loop from Run(), driving the wrapper from in-memory channels.
//
// Design invariants tested:
//
//  1. A detected prompt always produces exactly ONE approval keystroke.
//  2. A second prompt that arrives DURING a countdown is approved via the
//     post-watermark content check inside executeApproval.
//  3. A second prompt that arrives AFTER the buffer is cleared is approved via
//     the normal handleOutput detection path.
//  4. N sequential prompts each produce exactly N approvals (subagent scenario).
//  5. Starting a countdown while one is already active is idempotent (no
//     goroutine, no mutex, no deadlock possible in the new single-loop design).
//  6. Plain text never triggers a spurious approval.
package wrapper

import (
	"bytes"
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alehatsman/claude-autoapprove/internal/terminal"
)

// permissionDialog is the minimal text that IsPrompt scores >= 3.
const permissionDialog = " 1. Yes\n 2. No\n Enter to approve \n Esc to cancel\n"

// ─── Test infrastructure ───────────────────────────────────────────────────

// testEnv wires a ClaudeWrapper to in-memory channels so tests can inject PTY
// output and stdin keystrokes without a real process or terminal.
type testEnv struct {
	wrap    *ClaudeWrapper
	ptmxIn  chan []byte // send PTY bytes here  (simulates Claude output)
	stdinIn chan []byte // send stdin bytes here (simulates user keystrokes)
	reader  *os.File   // read approval bytes from here
}

// newTestEnv creates a fully-wired test environment and starts the event loop
// in a background goroutine that is cancelled when the test ends.
func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	wrap := NewWithConfig(&Config{CountdownSeconds: 1, BufferSize: 10_000})
	wrap.ptmx = wr
	wrap.term = terminal.New(nil)

	ptmxIn := make(chan []byte, 20)
	stdinIn := make(chan []byte, 20)

	ctx, cancel := context.WithCancel(context.Background())

	// testLoop mirrors the main select loop in Run() but reads from in-memory
	// channels instead of real I/O.  All wrapper state is owned by this goroutine.
	go func() {
		watchdog := time.NewTicker(500 * time.Millisecond)
		status := time.NewTicker(200 * time.Millisecond)
		defer watchdog.Stop()
		defer status.Stop()
		for {
			var countdownCh <-chan time.Time
			if wrap.countdownActive {
				remaining := time.Until(wrap.countdownEnd)
				if remaining <= 0 {
					wrap.executeApproval()
					if wrap.countdownActive {
						remaining = time.Until(wrap.countdownEnd)
						if remaining > 0 {
							countdownCh = time.After(remaining)
						}
					}
				} else {
					countdownCh = time.After(remaining)
				}
			}

			select {
			case <-ctx.Done():
				return
			case data := <-ptmxIn:
				wrap.handleOutput(data)
			case data := <-stdinIn:
				wrap.handleInput(data)
			case <-countdownCh:
				wrap.executeApproval()
			case <-watchdog.C:
				wrap.checkBuffer()
			case <-status.C:
				wrap.drawStatus()
			}
		}
	}()

	t.Cleanup(func() {
		cancel()
		wr.Close()
		r.Close()
	})

	return &testEnv{wrap: wrap, ptmxIn: ptmxIn, stdinIn: stdinIn, reader: r}
}

// inject sends PTY bytes to the test loop (simulates Claude output).
func (e *testEnv) inject(data []byte) { e.ptmxIn <- data }

// press sends a keystroke to the test loop (simulates user input).
func (e *testEnv) press(data []byte) { e.stdinIn <- data }

// approvalTracker counts '\r' bytes written to the pipe (one per approval).
type approvalTracker struct {
	n    atomic.Int32
	ping chan struct{}
}

func newApprovalTracker(r *os.File) *approvalTracker {
	at := &approvalTracker{ping: make(chan struct{}, 200)}
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := r.Read(buf)
			if err != nil {
				return
			}
			for _, b := range buf[:n] {
				if b == '\r' {
					at.n.Add(1)
					select {
					case at.ping <- struct{}{}:
					default:
					}
				}
			}
		}
	}()
	return at
}

// waitFor blocks until at least wantN approvals are counted, or timeout.
func (at *approvalTracker) waitFor(t *testing.T, wantN int, timeout time.Duration) bool {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		if int(at.n.Load()) >= wantN {
			return true
		}
		select {
		case <-at.ping:
			if int(at.n.Load()) >= wantN {
				return true
			}
		case <-timer.C:
			t.Errorf("timeout: wanted %d approvals, got %d", wantN, at.n.Load())
			return false
		}
	}
}

func (at *approvalTracker) count() int { return int(at.n.Load()) }

// ─── Tests ────────────────────────────────────────────────────────────────

// TestSinglePromptApproved: one dialog → exactly one approval keystroke.
func TestSinglePromptApproved(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.inject([]byte(permissionDialog))

	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	// Allow time for any spurious second approval.
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want exactly 1 approval, got %d (spurious double-approval?)", n)
	}
}

// TestNoFalsePositiveOnPlainText: plain code output must never trigger an approval.
func TestNoFalsePositiveOnPlainText(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	plainOutputs := []string{
		"Here is the code:\n```go\nfunc foo() {}\n```\n",
		"// This is a comment\n// with multiple lines\n",
		"Running: npm install\nfetching packages...\ndone\n",
		"Error: something went wrong\nStack trace:\n  at foo:1\n",
	}
	for _, s := range plainOutputs {
		env.inject([]byte(s))
	}

	time.Sleep(600 * time.Millisecond)
	if n := at.count(); n != 0 {
		t.Errorf("got %d unexpected approval(s) for plain text", n)
	}
}

// TestConsecutivePromptDuringCountdown: second dialog arrives while countdown
// for the first is running.  executeApproval checks post-watermark content and
// starts a new countdown for the second dialog.
func TestConsecutivePromptDuringCountdown(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	// Inject first dialog; pause briefly to let the countdown start before
	// injecting the second (simulates a second subagent asking during countdown).
	env.inject([]byte(permissionDialog))
	time.Sleep(50 * time.Millisecond)
	env.inject([]byte(permissionDialog))

	if !at.waitFor(t, 2, 6*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 2 {
		t.Errorf("want exactly 2 approvals, got %d", n)
	}
}

// TestPromptArrivesAfterBufferClear: second dialog after the buffer is cleared.
func TestPromptArrivesAfterBufferClear(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.inject([]byte(permissionDialog))
	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}

	// Buffer is cleared after approval; inject a new dialog.
	time.Sleep(50 * time.Millisecond)
	env.inject([]byte(permissionDialog))

	if !at.waitFor(t, 2, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 2 {
		t.Errorf("want exactly 2 approvals, got %d", n)
	}
}

// TestSequentialRapidFire: N dialogs, each appearing ~50 ms after the previous
// approval.  This is the primary real-world subagent scenario.
func TestSequentialRapidFire(t *testing.T) {
	const N = 5
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	go func() {
		env.inject([]byte(permissionDialog))
		prev := 0
		for i := 1; i < N; i++ {
			for int(at.n.Load()) <= prev {
				time.Sleep(10 * time.Millisecond)
			}
			prev = int(at.n.Load())
			time.Sleep(50 * time.Millisecond)
			env.inject([]byte(permissionDialog))
		}
	}()

	if !at.waitFor(t, N, time.Duration(N)*3*time.Second) {
		return
	}
	time.Sleep(300 * time.Millisecond)
	if n := at.count(); n != N {
		t.Errorf("want exactly %d approvals, got %d", N, n)
	}
}

// TestStartCountdownIdempotent: starting a countdown while one is already
// active is idempotent — no goroutine, no mutex, no deadlock possible.
// Both injected dialogs must produce at least one approval without hanging.
func TestStartCountdownIdempotent(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.inject([]byte(permissionDialog))
	env.inject([]byte(permissionDialog))

	// At least one approval within the deadline confirms no deadlock.
	at.waitFor(t, 1, 3*time.Second)
}

// TestAutoApproveDisabledNoApproval: when auto-approve is off, a detected
// prompt must never trigger a countdown or approval.
func TestAutoApproveDisabledNoApproval(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	// Toggle off via Ctrl+A through the normal input path (avoids data race).
	env.press([]byte{0x01})
	time.Sleep(30 * time.Millisecond)

	env.inject([]byte(permissionDialog))

	time.Sleep(600 * time.Millisecond)
	if n := at.count(); n != 0 {
		t.Errorf("got %d approval(s) while autoApprove=false", n)
	}
}

// TestWatermarkPreventsSpuriousApproval: verifies the bufferAtCountdownStart
// watermark.  Without it, approving dialog1 would re-detect the same dialog
// in the post-watermark content and fire a second spurious countdown.
func TestWatermarkPreventsSpuriousApproval(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.inject([]byte(permissionDialog))
	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}

	// Wait long enough for a spurious second countdown (1 s delay) to have
	// fired if the watermark were missing.
	time.Sleep(2 * time.Second)
	if n := at.count(); n != 1 {
		t.Errorf("watermark fix broken: want 1 approval, got %d (spurious re-approval)", n)
	}
}

// ─── raw-bytes tracker (used by TestNeedsYesApproval) ─────────────────────

type rawBytesTracker struct {
	mu    sync.Mutex
	data  []byte
	ready chan struct{}
}

func newRawBytesTracker(r *os.File) *rawBytesTracker {
	bt := &rawBytesTracker{ready: make(chan struct{}, 200)}
	go func() {
		buf := make([]byte, 64)
		for {
			n, err := r.Read(buf)
			if err != nil {
				return
			}
			bt.mu.Lock()
			bt.data = append(bt.data, buf[:n]...)
			bt.mu.Unlock()
			select {
			case bt.ready <- struct{}{}:
			default:
			}
		}
	}()
	return bt
}

func (bt *rawBytesTracker) waitForPattern(t *testing.T, pattern []byte, timeout time.Duration) bool {
	t.Helper()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	for {
		bt.mu.Lock()
		found := bytes.Contains(bt.data, pattern)
		bt.mu.Unlock()
		if found {
			return true
		}
		select {
		case <-bt.ready:
		case <-timer.C:
			bt.mu.Lock()
			t.Errorf("timeout: pattern %q not found in written bytes %q", pattern, bt.data)
			bt.mu.Unlock()
			return false
		}
	}
}

// ─── Additional edge-case tests ────────────────────────────────────────────

// TestNeedsYesApproval: a (y/n) prompt must cause the wrapper to write "yes\r"
// rather than just "\r".
func TestNeedsYesApproval(t *testing.T) {
	env := newTestEnv(t)
	bt := newRawBytesTracker(env.reader)

	const ynDialog = "Do you want to proceed? (y/n)"
	env.inject([]byte(ynDialog))

	if !bt.waitForPattern(t, []byte("yes"), 3*time.Second) {
		return
	}
	if !bt.waitForPattern(t, []byte("\r"), 3*time.Second) {
		return
	}
	bt.mu.Lock()
	yesIdx := bytes.Index(bt.data, []byte("yes"))
	crIdx := bytes.Index(bt.data, []byte("\r"))
	bt.mu.Unlock()
	if yesIdx > crIdx {
		t.Errorf("got \\r before 'yes': yesIdx=%d crIdx=%d", yesIdx, crIdx)
	}
}

// TestChunkedDialogDelivery: dialog split across two inject calls (fragmented
// PTY read).  Buffer accumulation must reassemble it into exactly one approval.
func TestChunkedDialogDelivery(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	mid := len(permissionDialog) / 2
	env.inject([]byte(permissionDialog[:mid]))
	time.Sleep(20 * time.Millisecond)
	env.inject([]byte(permissionDialog[mid:]))

	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want exactly 1 approval for chunked dialog, got %d", n)
	}
}

// TestLargeOutputBeforeDialog: 5 000 bytes of noise before the dialog.  The
// tail window in IsPrompt must still detect the dialog.
func TestLargeOutputBeforeDialog(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	noise := make([]byte, 5000)
	for i := range noise {
		noise[i] = 'x'
	}
	env.inject(noise)
	time.Sleep(10 * time.Millisecond)
	env.inject([]byte(permissionDialog))

	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want 1 approval after large output, got %d", n)
	}
}

// TestToggleReenableWithBufferedDialog: when auto-approve is disabled the dialog
// accumulates in the buffer without triggering a countdown.  Re-enabling via
// Ctrl+A must detect the buffered dialog and approve it.
func TestToggleReenableWithBufferedDialog(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	// Toggle off via Ctrl+A.
	env.press([]byte{0x01})
	time.Sleep(30 * time.Millisecond)

	env.inject([]byte(permissionDialog))
	time.Sleep(100 * time.Millisecond)

	if at.count() != 0 {
		t.Errorf("no approval expected while auto-approve is off, got %d", at.count())
	}

	// Re-enable: toggleAutoApprove detects the buffered dialog and starts countdown.
	env.press([]byte{0x01})

	if !at.waitFor(t, 1, 4*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want 1 approval after re-enable, got %d", n)
	}
}

// TestCountdownCancelThenRedetect: pressing a non-Enter key during a countdown
// cancels it.  The watchdog (500 ms) re-detects the still-buffered dialog and
// starts a new countdown → approval.
func TestCountdownCancelThenRedetect(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.inject([]byte(permissionDialog))
	time.Sleep(50 * time.Millisecond) // let countdown start

	// Cancel by pressing any non-Enter key.
	env.press([]byte{'x'})

	// Watchdog fires within 500 ms; countdown = 1 s → approval within ~2 s total.
	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want 1 approval after cancel+redetect, got %d", n)
	}
}

// TestHandleInputEmptySlice: sending an empty byte slice must not panic.
// handleInput guards with len(data)==0 check; test completes iff no panic.
func TestHandleInputEmptySlice(t *testing.T) {
	env := newTestEnv(t)
	env.press([]byte{})
	time.Sleep(100 * time.Millisecond) // let the loop process the empty slice
}

// TestPtmxWriteFailureNoRetryLoop: when ptmx.Write fails (ptmx closed), the
// wrapper must not enter an infinite retry loop — the test completes normally.
func TestPtmxWriteFailureNoRetryLoop(t *testing.T) {
	env := newTestEnv(t)
	// Close the write end so every Write call immediately returns an error.
	env.wrap.ptmx.Close()

	env.inject([]byte(permissionDialog))

	// Countdown = 1 s; allow 3 s for full flow.  An infinite retry loop would
	// keep the CPU busy and this test would time out under -timeout.
	time.Sleep(3 * time.Second)
}

// TestConcurrentRapidPromptsNoDeadlock: stress test — no deadlock, no panic.
// With -race, any data races are reported by the race detector.
func TestConcurrentRapidPromptsNoDeadlock(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	const N = 8
	for i := 0; i < N; i++ {
		env.inject([]byte(permissionDialog))
		time.Sleep(20 * time.Millisecond)
	}

	// At least one approval must fire without the system deadlocking.
	if !at.waitFor(t, 1, 5*time.Second) {
		return
	}
	time.Sleep(2 * time.Second)
	if n := at.count(); n < 1 {
		t.Errorf("expected at least 1 approval, got 0")
	}
}

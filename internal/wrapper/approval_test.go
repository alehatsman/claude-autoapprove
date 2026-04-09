// Package wrapper — approval logic tests.
//
// These tests exercise the countdown / approval state machine directly without
// a real PTY or Claude process.  A pipe replaces w.ptmx so that every byte
// written by countdownAndApprove (the approval keystrokes) can be observed and
// counted.  A stub Terminal is used so DrawStatus / ClearStatus are no-ops that
// merely write harmless ANSI to stderr.
//
// Design invariants tested:
//
//  1. A detected prompt always produces exactly ONE approval keystroke.
//  2. A second prompt that arrives DURING a countdown (scenario A) is approved
//     via the accumulated-buffer → recheckBuffer chain.
//  3. A second prompt that arrives AFTER the buffer is cleared (scenario C) is
//     approved via the normal handleClaudeOutput detection path or the
//     periodic watchdog.
//  4. N sequential prompts (each arriving after the previous approval) all
//     produce exactly N approvals — the realistic subagent scenario.
//  5. startCountdown called while a countdown is already running does NOT
//     deadlock (the old defer-Unlock-during-Wait bug).
//  6. The recheckBuffer channel correctly uses a text snapshot when provided,
//     and falls back to the live buffer when the string is empty.
//  7. Plain text never triggers a spurious approval.
package wrapper

import (
	"bytes"
	"context"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/alehatsman/claude-autoapprove/internal/detection"
	"github.com/alehatsman/claude-autoapprove/internal/terminal"
)

// permissionDialog is the minimal text that IsPrompt scores >= 3.
// It mirrors what Claude Code renders for a permission request.
const permissionDialog = " 1. Yes\n 2. No\n Enter to approve \n Esc to cancel\n"

// ─── Test infrastructure ───────────────────────────────────────────────────

// newTestWrapper creates a ClaudeWrapper ready for unit testing:
//   - CountdownSeconds = 1 (minimum) for speed
//   - ptmx backed by the write-end of a pipe so approval bytes are observable
//   - Terminal backed by nil ptmx (UpdateSize is never called in tests;
//     DrawStatus / ClearStatus write harmless ANSI to stderr)
func newTestWrapper(t *testing.T) (w *ClaudeWrapper, ptmxRead *os.File) {
	t.Helper()
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	wrap := NewWithConfig(&Config{CountdownSeconds: 1, BufferSize: 10_000})
	wrap.ptmx = wr
	wrap.term = terminal.New(nil)
	t.Cleanup(func() { wr.Close(); r.Close() })
	return wrap, r
}

// approvalTracker counts '\r' bytes written to the pipe (one per approval).
// It is safe to call waitFor from any goroutine.
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

// waitFor blocks until at least wantN approvals have been counted, or timeout.
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

// count returns the current approval count.
func (at *approvalTracker) count() int { return int(at.n.Load()) }

// autoApprover is a goroutine that immediately sends to countdownApproveNow
// whenever a countdown is running.  Returns a cancel func.
func autoApprover(w *ClaudeWrapper) func() {
	done := make(chan struct{})
	go func() {
		for {
			select {
			case <-done:
				return
			default:
			}
			w.countdownLock.Lock()
			running := w.countdownRunning
			ch := w.countdownApproveNow
			w.countdownLock.Unlock()
			if running {
				select {
				case ch <- struct{}{}:
				default:
				}
			}
			time.Sleep(5 * time.Millisecond)
		}
	}()
	return func() { close(done) }
}

// mainLoopSim mirrors the recheckBuffer case from Run()'s select loop.
// It must be running for consecutive-prompt detection to work.
func mainLoopSim(ctx context.Context, w *ClaudeWrapper) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case text := <-w.recheckBuffer:
				w.countdownLock.Lock()
				running := w.countdownRunning
				w.countdownLock.Unlock()
				if !w.autoApprove || running {
					continue
				}
				check := text
				if check == "" {
					w.bufferLock.Lock()
					check = w.buffer
					w.bufferLock.Unlock()
				}
				if check != "" {
					if ok, _ := detection.IsPrompt(check); ok {
						w.startCountdown()
					}
				}
			}
		}
	}()
}

// drainPipe discards all bytes from the pipe (used when we don't need counts).
func drainPipe(r *os.File) {
	go func() {
		buf := make([]byte, 64)
		for {
			if _, err := r.Read(buf); err != nil {
				return
			}
		}
	}()
}

// ─── Tests ────────────────────────────────────────────────────────────────

// TestSinglePromptApproved: one dialog → exactly one approval keystroke.
func TestSinglePromptApproved(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	w.handleClaudeOutput([]byte(permissionDialog))

	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}

	// Allow time for any spurious second approval (the old double-approval bug).
	// With the watermark fix, there must be exactly 1.
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want exactly 1 approval, got %d (spurious double-approval?)", n)
	}
}

// TestNoFalsePositiveOnPlainText: plain code output must never trigger an approval.
func TestNoFalsePositiveOnPlainText(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	plainOutputs := []string{
		"Here is the code:\n```go\nfunc foo() {}\n```\n",
		"// This is a comment\n// with multiple lines\n",
		"Running: npm install\nfetching packages...\ndone\n",
		"Error: something went wrong\nStack trace:\n  at foo:1\n",
	}
	for _, s := range plainOutputs {
		w.handleClaudeOutput([]byte(s))
	}

	time.Sleep(600 * time.Millisecond)
	if n := at.count(); n != 0 {
		t.Errorf("got %d unexpected approval(s) for plain text", n)
	}
}

// TestConsecutivePromptDuringCountdown: second dialog appears WHILE countdown for
// the first is running.  It must be captured via the accumulated-buffer path and
// produce a second approval.
//
// This is Scenario A from the analysis.
func TestConsecutivePromptDuringCountdown(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	// Trigger first countdown.
	w.handleClaudeOutput([]byte(permissionDialog))

	// Wait until countdown is confirmed running, then inject the second dialog
	// directly into the buffer — this simulates Claude writing dialog2 output
	// while countdownRunning=true (so handleClaudeOutput skips detection).
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		w.countdownLock.Lock()
		running := w.countdownRunning
		w.countdownLock.Unlock()
		if running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.countdownLock.Lock()
	if !w.countdownRunning {
		t.Fatal("countdown did not start within 200ms")
	}
	w.countdownLock.Unlock()

	// Inject dialog2 into the buffer (bypassing handleClaudeOutput detection,
	// which is what would happen in production since countdownRunning=true).
	w.bufferLock.Lock()
	w.buffer += permissionDialog
	w.bufferLock.Unlock()

	// Both dialogs must be approved: dialog1 via normal path,
	// dialog2 via accumulated-buffer → recheckBuffer → new countdown.
	if !at.waitFor(t, 2, 6*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 2 {
		t.Errorf("want exactly 2 approvals, got %d", n)
	}
}

// TestPromptArrivesAfterBufferClear: second dialog appears AFTER the buffer is
// cleared (post-approval).  handleClaudeOutput must detect it normally.
//
// This is Scenario C from the analysis — the most common real-world case.
func TestPromptArrivesAfterBufferClear(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	// First prompt.
	w.handleClaudeOutput([]byte(permissionDialog))
	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}

	// After approval, buffer is cleared.  Wait briefly, then inject dialog2.
	// At this point countdownRunning=false, so handleClaudeOutput detects normally.
	time.Sleep(50 * time.Millisecond)
	w.handleClaudeOutput([]byte(permissionDialog))

	if !at.waitFor(t, 2, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 2 {
		t.Errorf("want exactly 2 approvals, got %d", n)
	}
}

// TestSequentialRapidFire: N dialogs, each appearing ~50ms after the previous
// approval.  This is the primary real-world subagent scenario.  Each dialog
// must produce exactly one approval.
func TestSequentialRapidFire(t *testing.T) {
	const N = 5
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	// Feed dialogs sequentially, each after observing the previous approval.
	// We drive from a separate goroutine so waitFor can run on the test goroutine.
	go func() {
		w.handleClaudeOutput([]byte(permissionDialog))
		prev := 0
		for i := 1; i < N; i++ {
			// Spin until approval i arrives.
			for int(at.n.Load()) <= prev {
				time.Sleep(10 * time.Millisecond)
				if ctx.Err() != nil {
					return
				}
			}
			prev = int(at.n.Load())
			// Brief Claude processing delay before the next dialog appears.
			time.Sleep(50 * time.Millisecond)
			w.handleClaudeOutput([]byte(permissionDialog))
		}
	}()

	if !at.waitFor(t, N, time.Duration(N)*3*time.Second) {
		return
	}
	// Allow any spurious extra approval to surface.
	time.Sleep(300 * time.Millisecond)
	if n := at.count(); n != N {
		t.Errorf("want exactly %d approvals, got %d", N, n)
	}
}

// TestStartCountdownWhileRunningNoDeadlock: calling startCountdown while a
// countdown is already in progress must complete without blocking.
//
// The old code deadlocked here because startCountdown held countdownLock while
// calling countdownWg.Wait(); the running goroutine needed that same lock to
// set countdownRunning=false.
func TestStartCountdownWhileRunningNoDeadlock(t *testing.T) {
	w, r := newTestWrapper(t)
	drainPipe(r)

	// Start first countdown.
	w.handleClaudeOutput([]byte(permissionDialog))

	// Confirm it is running.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		w.countdownLock.Lock()
		running := w.countdownRunning
		w.countdownLock.Unlock()
		if running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.countdownLock.Lock()
	if !w.countdownRunning {
		t.Fatal("countdown did not start within 200ms")
	}
	w.countdownLock.Unlock()

	// Call startCountdown again from a goroutine (simulates the main loop
	// detecting a new prompt while one is already running).  Must not block.
	done := make(chan struct{})
	go func() {
		defer close(done)
		w.startCountdown() // old code deadlocked here
	}()

	select {
	case <-done:
		// Good: completed without deadlock.
	case <-time.After(5 * time.Second):
		t.Fatal("startCountdown deadlocked (blocked >5s while countdown running)")
	}

	// Clean up: cancel the running countdown.
	w.countdownLock.Lock()
	if w.countdownRunning {
		close(w.countdownCancelled)
		w.countdownLock.Unlock()
		w.countdownWg.Wait()
		w.countdownLock.Lock()
		w.countdownCancelled = make(chan struct{})
	}
	w.countdownLock.Unlock()
}

// TestRecheckBufferWithSnapshot: when recheckBuffer carries a non-empty text
// snapshot, the main loop must start a countdown based on that snapshot even if
// the live buffer is empty.
func TestRecheckBufferWithSnapshot(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	// Live buffer is empty; snapshot contains a dialog.
	select {
	case w.recheckBuffer <- permissionDialog:
	default:
		t.Fatal("recheckBuffer unexpectedly full")
	}

	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want exactly 1 approval from snapshot, got %d", n)
	}
}

// TestRecheckBufferEmptyFallsBackToLiveBuffer: when recheckBuffer carries an
// empty string, the main loop must fall back to the current live buffer.
func TestRecheckBufferEmptyFallsBackToLiveBuffer(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	// Place a dialog in the live buffer WITHOUT going through handleClaudeOutput
	// (which would start a countdown on its own).
	w.bufferLock.Lock()
	w.buffer = permissionDialog
	w.bufferLock.Unlock()

	// Signal with empty string — must fall back to live buffer.
	select {
	case w.recheckBuffer <- "":
	default:
		t.Fatal("recheckBuffer unexpectedly full")
	}

	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want exactly 1 approval, got %d", n)
	}
}

// TestAutoApproveDisabledNoApproval: when autoApprove is off, a detected prompt
// must never trigger a countdown or approval.
func TestAutoApproveDisabledNoApproval(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	w.autoApprove = false
	w.handleClaudeOutput([]byte(permissionDialog))

	time.Sleep(600 * time.Millisecond)
	if n := at.count(); n != 0 {
		t.Errorf("got %d approval(s) while autoApprove=false", n)
	}
}

// TestWatermarkPreventsSpuriousApprovalAfterSinglePrompt: verifies the
// bufferAtCountdownStart watermark fix.  Without it, approving dialog1 would
// re-detect dialog1 in the accumulated buffer and fire a second spurious
// countdown.  The invariant: N distinct prompts → exactly N approvals.
//
// This is specifically testing the regression that was fixed by the watermark.
func TestWatermarkPreventsSpuriousApproval(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	// One prompt, should produce exactly one approval.
	w.handleClaudeOutput([]byte(permissionDialog))
	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}

	// Wait long enough for a spurious second countdown (1s delay + 300ms status)
	// to have fired if the watermark were missing.
	time.Sleep(2 * time.Second)
	if n := at.count(); n != 1 {
		t.Errorf("watermark fix broken: want 1 approval, got %d (spurious re-approval)", n)
	}
}

// ─── raw-bytes tracker (used by TestNeedsYesApproval) ─────────────────────

// rawBytesTracker records every byte written to the pipe so tests can verify
// the exact byte sequence (e.g. "yes\r" for (y/n) prompts).
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
// to the PTY rather than just "\r".
//
// NeedsYes matches `(?i)Type.*yes|Enter.*yes|\(y/n\)`.
// The yn_prompt IsPrompt pattern scores 3 (threshold met).
func TestNeedsYesApproval(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	bt := newRawBytesTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	// Dialog that IsPrompt detects (score=3 via yn_prompt) and NeedsYes detects.
	const ynDialog = "Do you want to proceed? (y/n)"
	w.handleClaudeOutput([]byte(ynDialog))

	// Must see "yes" followed by "\r" in the pipe bytes.
	if !bt.waitForPattern(t, []byte("yes"), 3*time.Second) {
		return
	}
	if !bt.waitForPattern(t, []byte("\r"), 3*time.Second) {
		return
	}
	// Verify ordering: "yes" comes before "\r"
	bt.mu.Lock()
	yesIdx := bytes.Index(bt.data, []byte("yes"))
	crIdx := bytes.Index(bt.data, []byte("\r"))
	bt.mu.Unlock()
	if yesIdx > crIdx {
		t.Errorf("got \\r before 'yes': yesIdx=%d crIdx=%d", yesIdx, crIdx)
	}
}

// TestChunkedDialogDelivery: the dialog arrives split across two handleClaudeOutput
// calls (simulating a fragmented PTY read).  The buffer accumulation must reassemble
// it and trigger exactly one approval.
func TestChunkedDialogDelivery(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	mid := len(permissionDialog) / 2
	w.handleClaudeOutput([]byte(permissionDialog[:mid]))
	time.Sleep(20 * time.Millisecond)
	w.handleClaudeOutput([]byte(permissionDialog[mid:]))

	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want exactly 1 approval for chunked dialog, got %d", n)
	}
}

// TestLargeOutputBeforeDialog: 5 000 bytes of noise before the dialog.  IsPrompt
// uses a 3 000-char tail window, so only the tail is scored.  The dialog must still
// be detected and produce exactly one approval.
func TestLargeOutputBeforeDialog(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	noise := make([]byte, 5000)
	for i := range noise {
		noise[i] = 'x'
	}
	w.handleClaudeOutput(noise)
	time.Sleep(10 * time.Millisecond)
	w.handleClaudeOutput([]byte(permissionDialog))

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
// toggleAutoApprove must detect the buffered dialog and approve it.
func TestToggleReenableWithBufferedDialog(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	// Disable and let dialog accumulate.
	w.autoApprove = false
	w.handleClaudeOutput([]byte(permissionDialog))

	time.Sleep(100 * time.Millisecond)
	if at.count() != 0 {
		t.Errorf("no approval expected while auto-approve is off, got %d", at.count())
	}

	// toggleAutoApprove re-enables and immediately checks the buffer.
	w.toggleAutoApprove()

	// toggleAutoApprove sleeps 800 ms internally; countdown adds ~0 ms (autoApprover
	// fires immediately) + 300 ms draw sleep → allow 4 s total.
	if !at.waitFor(t, 1, 4*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want 1 approval after re-enable, got %d", n)
	}
}

// TestCountdownCancelThenRedetect: cancelling an active countdown (any-key-press)
// must schedule a recheckBuffer signal so the still-visible dialog is re-detected
// and approved by the subsequent countdown.
func TestCountdownCancelThenRedetect(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	// Start first countdown.
	w.handleClaudeOutput([]byte(permissionDialog))

	// Wait until countdown is running.
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		w.countdownLock.Lock()
		running := w.countdownRunning
		w.countdownLock.Unlock()
		if running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}
	w.countdownLock.Lock()
	if !w.countdownRunning {
		t.Fatal("countdown did not start within 200ms")
	}
	w.countdownLock.Unlock()

	// Cancel (simulate any-key-press) by closing the cancellation channel.
	// countdownAndApprove's cancelled path then schedules recheckBuffer<-"" after 500 ms.
	w.countdownLock.Lock()
	close(w.countdownCancelled)
	w.countdownCancelled = make(chan struct{})
	w.countdownLock.Unlock()
	w.countdownWg.Wait()

	// Dialog is still in the buffer.  After 500 ms the recheckBuffer fires,
	// mainLoopSim detects the dialog and starts a new countdown, which autoApprover
	// immediately fires.  Allow 3 s for the whole chain.
	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want 1 approval after cancel+redetect, got %d", n)
	}
}

// TestHandleUserInputEmptySlice: calling handleUserInput with an empty slice while
// a countdown is running must NOT panic (index out of range on data[0]).
//
// This documents a real bug: the condition `countdownRunning && (data[0] == '\r'...)`
// is evaluated without a prior length guard.
func TestHandleUserInputEmptySlice(t *testing.T) {
	w, r := newTestWrapper(t)
	drainPipe(r)

	// Cancel countdown on test exit so the goroutine does not outlive the test.
	t.Cleanup(func() {
		w.countdownLock.Lock()
		if w.countdownRunning {
			close(w.countdownCancelled)
			w.countdownLock.Unlock()
			w.countdownWg.Wait()
		} else {
			w.countdownLock.Unlock()
		}
	})

	// Start a countdown so countdownRunning=true (the condition that exposes the bug).
	w.handleClaudeOutput([]byte(permissionDialog))
	deadline := time.Now().Add(200 * time.Millisecond)
	for time.Now().Before(deadline) {
		w.countdownLock.Lock()
		running := w.countdownRunning
		w.countdownLock.Unlock()
		if running {
			break
		}
		time.Sleep(5 * time.Millisecond)
	}

	// Wrap in anonymous function to catch panic without crashing the test binary.
	panicked := false
	func() {
		defer func() {
			if rec := recover(); rec != nil {
				panicked = true
			}
		}()
		w.handleUserInput([]byte{})
	}()

	if panicked {
		t.Error("handleUserInput panicked on empty slice with countdownRunning=true (index-out-of-range bug)")
	}
}

// TestPtmxWriteFailureNoRetryLoop: if writing approval bytes to the PTY fails
// (e.g. the Claude process already exited), the wrapper must log the error and
// exit the countdown goroutine without entering an infinite retry loop.
func TestPtmxWriteFailureNoRetryLoop(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	mainLoopSim(ctx, w)

	// Close the write-end before triggering approval so writes fail immediately.
	// The Cleanup registered by newTestWrapper calls Close() again — harmless.
	w.ptmx.Close()
	_ = r // read-end: no tracker needed since no successful write can happen

	stop := autoApprover(w)
	defer stop()

	w.handleClaudeOutput([]byte(permissionDialog))

	// The countdown goroutine must detect the write error, set countdownRunning=false,
	// and return (after its 1 s error-display sleep).  Verify within 5 s.
	ok := false
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		w.countdownLock.Lock()
		running := w.countdownRunning
		w.countdownLock.Unlock()
		if !running {
			ok = true
			break
		}
		time.Sleep(20 * time.Millisecond)
	}
	if !ok {
		t.Error("countdown goroutine did not finish after ptmx write failure within 5 s (infinite retry?)")
	}
}

// TestConcurrentRapidPromptsNoDeadlock: stress test that hammers the system with
// rapid prompt injections.  With -race, any data races will be reported.
// Primary goal: no deadlock, no panic.
func TestConcurrentRapidPromptsNoDeadlock(t *testing.T) {
	w, r := newTestWrapper(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	at := newApprovalTracker(r)
	mainLoopSim(ctx, w)
	stop := autoApprover(w)
	defer stop()

	const N = 8
	for i := 0; i < N; i++ {
		w.handleClaudeOutput([]byte(permissionDialog))
		time.Sleep(20 * time.Millisecond)
	}

	// We don't assert an exact count — overlapping injections mean some are
	// deduplicated.  Assert: at least 1 approval happened and the system did
	// not deadlock (waitFor would time out if it had).
	if !at.waitFor(t, 1, 5*time.Second) {
		return
	}

	// Give the last countdown goroutine time to finish cleanly.
	// We avoid calling countdownWg.Wait() from a goroutine here because that
	// races with concurrent Add() calls inside startCountdown.
	time.Sleep(2 * time.Second)

	if n := at.count(); n < 1 {
		t.Errorf("expected at least 1 approval, got 0")
	}
}

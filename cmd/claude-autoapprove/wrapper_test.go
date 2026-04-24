package main

import (
	"bytes"
	"os"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

const permissionDialog = " 1. Yes\n 2. No\n Enter to approve \n Esc to cancel\n"

// ── Test infrastructure ───────────────────────────────────────────────────────

type testEnv struct {
	wrap    *ClaudeWrapper
	ptmxIn  chan []byte
	stdinIn chan []byte
	reader  *os.File
}

func newTestEnv(t *testing.T) *testEnv {
	t.Helper()
	r, wr, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}

	wrap := NewWithConfig(&Config{CountdownSeconds: 1})
	wrap.ptmx = wr
	wrap.term = newTerminal(nil)

	ptmxIn := make(chan []byte, 20)
	stdinIn := make(chan []byte, 20)

	done := make(chan struct{})
	t.Cleanup(func() {
		close(done)
		wr.Close()
		r.Close()
	})

	go func() {
		watchdog := time.NewTicker(500 * time.Millisecond)
		status := time.NewTicker(200 * time.Millisecond)
		defer watchdog.Stop()
		defer status.Stop()
		for {
			select {
			case <-done:
				return
			case data := <-ptmxIn:
				wrap.handleOutput(data)
			case data := <-stdinIn:
				wrap.handleInput(data)
			case <-watchdog.C:
				wrap.checkBuffer()
			case <-status.C:
				if wrap.countdownActive && !time.Now().Before(wrap.countdownEnd) {
					wrap.executeApproval()
				}
				wrap.drawStatus()
			}
		}
	}()

	return &testEnv{wrap: wrap, ptmxIn: ptmxIn, stdinIn: stdinIn, reader: r}
}

func (e *testEnv) inject(data []byte) { e.ptmxIn <- data }
func (e *testEnv) press(data []byte)  { e.stdinIn <- data }

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

// ── Config tests ──────────────────────────────────────────────────────────────

func TestNewWithConfig(t *testing.T) {
	tests := []struct {
		name          string
		config        *Config
		wantCountdown int
	}{
		{"Default config", nil, 1},
		{"Custom countdown", &Config{CountdownSeconds: 1}, 1},
		{"Zero countdown allowed", &Config{CountdownSeconds: 0}, 0},
		{"Negative gets default", &Config{CountdownSeconds: -1}, 1},
		{"Large countdown allowed", &Config{CountdownSeconds: 60}, 60},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			w := NewWithConfig(tt.config)
			if w.countdownSeconds != tt.wantCountdown {
				t.Errorf("countdownSeconds = %d, want %d", w.countdownSeconds, tt.wantCountdown)
			}
			if !w.autoApprove {
				t.Error("autoApprove should be true by default")
			}
		})
	}
}

func TestNew(t *testing.T) {
	w := New()
	if w.countdownSeconds != 1 {
		t.Errorf("New() countdown = %d, want 1", w.countdownSeconds)
	}
	if !w.autoApprove {
		t.Error("autoApprove should be true")
	}
}

// ── Approval tests ────────────────────────────────────────────────────────────

func TestSinglePromptApproved(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.inject([]byte(permissionDialog))

	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want exactly 1 approval, got %d", n)
	}
}

func TestNoFalsePositiveOnPlainText(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	for _, s := range []string{
		"Here is the code:\n```go\nfunc foo() {}\n```\n",
		"// This is a comment\n// with multiple lines\n",
		"Running: npm install\nfetching packages...\ndone\n",
	} {
		env.inject([]byte(s))
	}

	time.Sleep(600 * time.Millisecond)
	if n := at.count(); n != 0 {
		t.Errorf("got %d unexpected approval(s) for plain text", n)
	}
}

func TestConsecutivePromptDuringCountdown(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.inject([]byte(permissionDialog))
	time.Sleep(50 * time.Millisecond)
	env.inject([]byte(permissionDialog))

	if !at.waitFor(t, 2, 6*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 2 {
		t.Errorf("want 2 approvals, got %d", n)
	}
}

func TestPromptArrivesAfterBufferClear(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.inject([]byte(permissionDialog))
	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	time.Sleep(50 * time.Millisecond)
	env.inject([]byte(permissionDialog))

	if !at.waitFor(t, 2, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 2 {
		t.Errorf("want 2 approvals, got %d", n)
	}
}

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
		t.Errorf("want %d approvals, got %d", N, n)
	}
}

func TestAutoApproveDisabledNoApproval(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.press([]byte{0x01}) // Ctrl+A — toggle off
	time.Sleep(30 * time.Millisecond)
	env.inject([]byte(permissionDialog))

	time.Sleep(600 * time.Millisecond)
	if n := at.count(); n != 0 {
		t.Errorf("got %d approval(s) while autoApprove=false", n)
	}
}

func TestWatermarkPreventsSpuriousApproval(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.inject([]byte(permissionDialog))
	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}

	time.Sleep(2 * time.Second)
	if n := at.count(); n != 1 {
		t.Errorf("watermark broken: want 1 approval, got %d", n)
	}
}

// rawBytesTracker captures all raw bytes written to the pipe.
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
			t.Errorf("timeout: %q not found in %q", pattern, bt.data)
			bt.mu.Unlock()
			return false
		}
	}
}

func TestNeedsYesApproval(t *testing.T) {
	env := newTestEnv(t)
	bt := newRawBytesTracker(env.reader)

	env.inject([]byte("Do you want to proceed? (y/n)"))

	if !bt.waitForPattern(t, []byte("yes"), 3*time.Second) {
		return
	}
	bt.mu.Lock()
	yesIdx := bytes.Index(bt.data, []byte("yes"))
	crIdx := bytes.Index(bt.data, []byte("\r"))
	bt.mu.Unlock()
	if yesIdx > crIdx {
		t.Errorf("\\r before 'yes': yesIdx=%d crIdx=%d", yesIdx, crIdx)
	}
}

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
		t.Errorf("want 1 approval for chunked dialog, got %d", n)
	}
}

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
		t.Errorf("want 1 approval, got %d", n)
	}
}

func TestToggleReenableWithBufferedDialog(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.press([]byte{0x01}) // toggle off
	time.Sleep(30 * time.Millisecond)
	env.inject([]byte(permissionDialog))
	time.Sleep(100 * time.Millisecond)

	if at.count() != 0 {
		t.Errorf("no approval expected while off, got %d", at.count())
	}

	env.press([]byte{0x01}) // toggle on

	if !at.waitFor(t, 1, 4*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want 1 approval after re-enable, got %d", n)
	}
}

func TestCountdownCancelThenRedetect(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	env.inject([]byte(permissionDialog))
	time.Sleep(50 * time.Millisecond)
	env.press([]byte{'x'}) // cancel

	if !at.waitFor(t, 1, 3*time.Second) {
		return
	}
	time.Sleep(200 * time.Millisecond)
	if n := at.count(); n != 1 {
		t.Errorf("want 1 approval after cancel+redetect, got %d", n)
	}
}

func TestHandleInputEmptySlice(t *testing.T) {
	env := newTestEnv(t)
	env.press([]byte{})
	time.Sleep(100 * time.Millisecond)
}

func TestPtmxWriteFailureNoRetryLoop(t *testing.T) {
	env := newTestEnv(t)
	env.wrap.ptmx.Close()
	env.inject([]byte(permissionDialog))
	time.Sleep(3 * time.Second)
}

func TestConcurrentRapidPromptsNoDeadlock(t *testing.T) {
	env := newTestEnv(t)
	at := newApprovalTracker(env.reader)

	for i := 0; i < 8; i++ {
		env.inject([]byte(permissionDialog))
		time.Sleep(20 * time.Millisecond)
	}

	if !at.waitFor(t, 1, 5*time.Second) {
		return
	}
	time.Sleep(2 * time.Second)
	if n := at.count(); n < 1 {
		t.Errorf("expected at least 1 approval, got 0")
	}
}

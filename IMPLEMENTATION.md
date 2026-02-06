# Implementation Details

## Architecture Overview

The auto-approve wrapper is built as a single Go binary using a PTY (pseudo-terminal) to provide transparent terminal behavior. The architecture consists of:

1. **PTY Layer**: Uses `github.com/creack/pty` to spawn the child process
2. **Output Monitor**: Goroutine continuously reads PTY output
3. **State Machine**: Main event loop manages state transitions
4. **Pattern Matchers**: Regex-based detection for prompts and dangers
5. **Input Injector**: Rate-limited input sender

## Key Design Decisions

### 1. PTY vs Pipes

**Choice**: PTY (pseudo-terminal)

**Rationale**:
- Interactive programs behave differently with pipes vs terminals
- PTY preserves terminal escape sequences, colors, and formatting
- Programs detect TTY and enable interactive features
- Window size (SIGWINCH) propagates correctly

**Implementation**:
```go
cmd := exec.Command(command, args...)
ptmx, err := pty.Start(cmd)
```

### 2. State Machine Architecture

**States**:
- `RUNNING`: Normal monitoring
- `WAITING_FOR_PROMPT`: Brief state after detection (not currently used explicitly, but reserved for future enhancements)
- `IDLE_NUDGE`: Performing nudge sequence
- `MANUAL_MODE`: Safety override, no auto-approval

**Transitions**:
- RUNNING → IDLE_NUDGE: idle timeout exceeded
- RUNNING → MANUAL_MODE: dangerous pattern detected OR user interrupt
- IDLE_NUDGE → RUNNING: nudge sequence complete
- IDLE_NUDGE → MANUAL_MODE: max retries exceeded

### 3. Circular Buffer for Pattern Matching

**Choice**: 4KB circular buffer

**Rationale**:
- Prompts may span multiple reads
- Need context around prompt (not just current chunk)
- Limit memory usage
- 4KB sufficient for typical prompts (usually < 200 bytes)

**Implementation**:
```go
type CircularBuffer struct {
    data []byte
    size int
    mu   sync.Mutex
}
```

### 4. Two-Level Rate Limiting

**Global Limit** (500ms default):
- Prevents any input send < 500ms after previous
- Protects against runaway approval loops
- Independent of rule type

**Per-Rule Cooldown** (1-2s default):
- Prevents same pattern from triggering repeatedly
- Different rules can have different cooldowns
- Press Enter: 1s (fast)
- Yes/No: 2s (more careful)

**Implementation**:
```go
// Global check
func (w *Wrapper) canSend() bool {
    return time.Since(w.lastSend) >= w.config.MinSendInterval
}

// Per-rule check
if !rule.lastSent.IsZero() && now.Sub(rule.lastSent) < rule.Cooldown {
    continue
}
```

## Heuristics Explained

### 1. "Waiting for Input" Detection

**Heuristic**: Recent output buffer contains prompt regex pattern

**Algorithm**:
1. Maintain 4KB circular buffer of recent output
2. On each read, append to buffer
3. Check all prompt regexes against buffer content
4. If match found AND cooldown expired → send response

**Why it works**:
- Prompts usually end with distinctive patterns (`[y/n]`, `? `)
- Checking buffer vs single chunk catches multi-read prompts
- Cooldown prevents double-triggering on single prompt

**False positives**:
- Prompt-like text in normal output (mitigated by specific regexes)
- Log messages containing "[y/n]" (mitigated by cooldown)

**False negatives**:
- Novel prompt formats not in rule list (user must add rule)
- Very long prompts > 4KB (extremely rare)

### 2. "No Output Activity" Detection

**Heuristic**: time.Since(lastOutput) > idle_timeout

**Algorithm**:
1. Update `lastOutput` timestamp on every PTY read
2. Every 500ms (ticker), check `time.Since(lastOutput)`
3. If > `idle_timeout` (default 15s) → trigger nudge
4. Reset timer on any output (even single byte)

**Why it works**:
- True hangs: no output for extended period
- Legitimate delays (thinking, computation): Claude eventually outputs progress
- 15s is long enough to avoid false positives during normal pauses

**Tuning**:
- Too short: false positives during slow operations
- Too long: user waits unnecessarily when actually stuck
- 15s is empirically good balance

### 3. "Dangerous" Command Detection

**Heuristic**: Output contains regex from denylist

**Algorithm**:
1. Maintain list of dangerous patterns (compiled regexes)
2. On every output chunk, check all patterns
3. If any match → immediate MANUAL_MODE
4. No auto-approval from that point forward

**Patterns include**:
- Destructive: `rm -rf /`, `mkfs`, `dd if=`
- System control: `shutdown`, `reboot`
- Exploits: fork bombs, pipe-to-shell
- Privilege escalation: `/etc/sudoers`, `chmod 777 /`
- Source control: `git push --force`

**Why it works**:
- Detects dangerous commands BEFORE approval prompt
- Conservative: false positives okay (just requires manual approval)
- Pattern-based is fast and doesn't require understanding context

**Limitations**:
- Obfuscated commands may bypass
- Context-dependent danger (e.g., `rm -rf /tmp/foo` is safe)
- Trade-off: safety over automation

## Edge Cases Handled

### 1. Race Condition: Quick Approval → No Response

**Problem**: User/wrapper approves, but Claude doesn't continue

**Root causes**:
- Network latency in Claude API
- Output buffering delays
- Claude internal state machine issues

**Solution**: Idle timeout + nudge sequence

**Flow**:
1. Approval sent at T+0
2. No output received
3. T+15s: idle timeout triggers
4. Send `\n` (wake up)
5. T+16s: send `y\n` (re-approve)
6. T+17s: send `continue\n` (explicit continue)
7. T+19s: return to RUNNING

**Why this works**:
- Multiple nudge types cover different stuck states
- Delays prevent overwhelming Claude
- Harmless if Claude wasn't stuck (extra newlines ignored)

### 2. Late Output Arrival

**Problem**: No output, wrapper starts nudging, then output arrives

**Solution**: Nudge sequence is idempotent and safe

**Flow**:
1. T+0: Last output
2. T+15: Nudge triggered, send `\n`
3. T+15.5: Output arrives (Claude was just slow)
4. T+16: Send `y\n` anyway (harmless)
5. Wrapper sees new output, resets idle timer
6. No harm done

**Why this works**:
- Extra newlines/y's are benign when not at prompt
- Once output resumes, idle timer resets
- Won't re-nudge unless another 15s of silence

### 3. Infinite Prompt Loop

**Problem**: Prompt triggers repeatedly (bad regex or Claude stuck loop)

**Solution**: Per-rule cooldown + max retries

**Protection layers**:
1. **Global rate limit**: Max 1 send per 500ms
2. **Per-rule cooldown**: Max 1 trigger per rule per 2s
3. **Nudge max retries**: Only 3 nudge attempts before MANUAL_MODE

**Example**:
- Bad regex matches every line
- First match: sends response
- Cooldown active for 2s
- Even if matches continue, won't send again for 2s
- After 3 idle nudges, gives up → MANUAL_MODE

### 4. Terminal Size Changes

**Problem**: User resizes terminal window

**Solution**: SIGWINCH signal handling

**Implementation**:
```go
ch := make(chan os.Signal, 1)
signal.Notify(ch, syscall.SIGWINCH)
go func() {
    for range ch {
        pty.InheritSize(os.Stdin, ptmx)
    }
}()
ch <- syscall.SIGWINCH // Initial resize
```

This ensures child process sees correct terminal dimensions.

### 5. User Interrupt (Ctrl+C)

**Problem**: User wants to take control

**Solution**: SIGINT handler

**Implementation**:
```go
sigCh := make(chan os.Signal, 1)
signal.Notify(sigCh, os.Interrupt, syscall.SIGTERM)
go func() {
    <-sigCh
    w.setState(StateManualMode)
}()
```

Signal still propagates to child, but wrapper stops auto-approving.

## Concurrency & Synchronization

### Goroutines:
1. **Main goroutine**: Event loop, state machine
2. **PTY reader**: Continuously reads output
3. **SIGWINCH handler**: Terminal resize
4. **Signal handler**: User interrupt

### Synchronization:
- `stateMu`: Protects state transitions
- `lastOutputMu`: Protects lastOutput timestamp
- `lastSendMu`: Protects lastSend timestamp
- `CircularBuffer.mu`: Protects buffer writes
- Channels: `outputCh`, `errCh` for goroutine communication

### Why RWMutex for some fields:
- `getState()` called frequently (read-heavy)
- `getTimeSinceLastOutput()` called every 500ms
- RWMutex allows concurrent reads

## Performance Characteristics

### Memory:
- Circular buffer: 4KB
- Output channel: 100 slots × ~4KB = ~400KB max
- Compiled regexes: ~20 patterns × ~1KB = ~20KB
- Total: < 1MB overhead

### CPU:
- Regex matching: O(n) per pattern per output chunk
- 5 default rules + 10 danger patterns = 15 checks
- On 4KB chunk: ~15 × O(4096) = negligible
- Ticker: 500ms interval (minimal overhead)

### Latency:
- Output pass-through: < 1ms (direct write to stdout)
- Prompt detection: < 5ms (regex matching)
- Response send: < 1ms (PTY write)
- Total added latency: < 10ms (imperceptible)

## Testing Strategy

### Unit Testing (not implemented, but recommendations):
1. Circular buffer: write/read patterns
2. Regex patterns: test prompts
3. Rate limiting: time-based tests
4. State transitions: state machine tests

### Integration Testing:
1. Mock prompting program: `read -p "Continue? [y/n]"`
2. Test idle timeout: program that sleeps 20s
3. Test dangerous patterns: echo commands with patterns
4. Test signal handling: send SIGINT during run

### Manual Testing:
```bash
# Test prompt detection
./autoapprove -- bash -c 'read -p "Continue? [y/n] " x; echo "Got: $x"'

# Test idle timeout
./autoapprove --idle 5s -- bash -c 'echo "Starting..."; sleep 10; echo "Done"'

# Test dangerous detection
./autoapprove -- bash -c 'echo "About to run: rm -rf /"; sleep 5'
```

## Future Enhancements

1. **Machine Learning**: Learn from user corrections
2. **Context-Aware Danger Detection**: Analyze command context
3. **Adaptive Timeouts**: Adjust based on historical patterns
4. **Prompt History**: Remember which prompts were approved
5. **Dry-Run Mode**: Log what would be approved without sending
6. **Metrics**: Export Prometheus metrics (approvals, nudges, etc.)
7. **Interactive Config**: Hot-reload rules.yaml without restart
8. **Scripting**: Lua/JavaScript hooks for custom logic

## Security Considerations

1. **Command Injection**: Wrapper doesn't parse commands, just passes through
2. **Regex DoS**: Use simple patterns, avoid catastrophic backtracking
3. **Resource Limits**: Circular buffer bounded, channel buffered
4. **Privilege Escalation**: Runs with same privileges as user
5. **Signal Handling**: Properly cleanup on exit

## Dependencies

- `github.com/creack/pty`: PTY handling (proven, widely used)
- `gopkg.in/yaml.v3`: Config parsing (standard)
- Go standard library: Everything else

Total: 2 external dependencies (minimal)

## Build Artifacts

- Single static binary: ~3.7MB
- No runtime dependencies
- No configuration required (defaults work)
- Cross-platform: Linux, macOS, BSD (anywhere PTY works)

## Maintenance

- Update danger patterns as new threats emerge
- Add prompt rules for new Claude prompt formats
- Tune default timeouts based on user feedback
- Monitor for regex pattern improvements

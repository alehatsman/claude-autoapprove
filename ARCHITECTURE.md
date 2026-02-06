# Architecture Diagram

## High-Level Flow

```
┌─────────────────────────────────────────────────────────────────┐
│                         User Terminal                            │
└──────────────────────────────┬──────────────────────────────────┘
                               │
                               │ spawns
                               ↓
┌─────────────────────────────────────────────────────────────────┐
│                      AutoApprove Wrapper                         │
│                                                                   │
│  ┌──────────────┐      ┌───────────────┐      ┌──────────────┐ │
│  │   PTY Layer  │─────→│ State Machine │─────→│Input Injector│ │
│  └──────────────┘      └───────────────┘      └──────────────┘ │
│         ↓                      ↑                       │         │
│  ┌──────────────┐      ┌───────────────┐              │         │
│  │Output Monitor│─────→│Pattern Matcher│              │         │
│  └──────────────┘      └───────────────┘              │         │
│         ↓                      ↑                       ↓         │
│  ┌──────────────────────────────────────────────────────────┐  │
│  │              Circular Buffer (4KB)                        │  │
│  └──────────────────────────────────────────────────────────┘  │
│         ↓                                                        │
│  ┌──────────────┐      ┌───────────────┐                       │
│  │ Logger       │      │Danger Detector│                       │
│  └──────────────┘      └───────────────┘                       │
└──────────────────────────────┬──────────────────────────────────┘
                               │ controls via PTY
                               ↓
┌─────────────────────────────────────────────────────────────────┐
│                      Claude CLI Process                          │
│                     (running in PTY)                             │
└─────────────────────────────────────────────────────────────────┘
```

## Component Details

### PTY Layer
- **Responsibility**: Spawn child process in pseudo-terminal
- **Technology**: `github.com/creack/pty`
- **Key Operations**:
  - `pty.Start(cmd)` - Create PTY and start process
  - `pty.InheritSize()` - Handle window resizing
  - Read/Write to PTY file descriptor

### Output Monitor (Goroutine)
- **Responsibility**: Continuously read PTY output
- **Operation**: Non-blocking reads in tight loop
- **Actions**:
  1. Read chunk from PTY (up to 4KB)
  2. Write to stdout (passthrough)
  3. Append to circular buffer
  4. Send to main loop via channel
  5. Update lastOutput timestamp

### Circular Buffer
- **Responsibility**: Maintain recent output for pattern matching
- **Size**: 4KB (configurable)
- **Thread-safe**: Protected by mutex
- **Why**: Prompts may span multiple reads

### Pattern Matcher
- **Responsibility**: Detect prompts and dangerous commands
- **Inputs**: Buffer content
- **Patterns**:
  - Prompt regexes (from config)
  - Danger regexes (hardcoded)
- **Output**: Matched rule or danger detection

### State Machine
- **Responsibility**: Manage wrapper behavior
- **States**:
  ```
  RUNNING ──────┐
     ↓          │
     │(idle)    │(danger/interrupt)
     ↓          ↓
  IDLE_NUDGE → MANUAL_MODE
  ```
- **Transitions**:
  - RUNNING → IDLE_NUDGE: No output for `idle_timeout`
  - IDLE_NUDGE → RUNNING: Nudge complete
  - IDLE_NUDGE → MANUAL: Max retries exceeded
  - ANY → MANUAL: Danger detected or Ctrl+C

### Input Injector
- **Responsibility**: Send input to PTY
- **Rate Limiting**:
  - Global: Min 500ms between any sends
  - Per-rule: Cooldown (1-3s) per pattern
- **Actions**:
  1. Check rate limits
  2. Write to PTY
  3. Update timestamps
  4. Log action

### Danger Detector
- **Responsibility**: Identify destructive commands
- **Patterns**:
  - `rm -rf /`
  - `mkfs`, `dd if=`
  - `shutdown`, `reboot`
  - Fork bombs, pipe-to-shell
  - etc.
- **Action**: Immediate MANUAL_MODE transition

### Logger
- **Responsibility**: Record all events
- **Format**: `[AUTOAPPROVE] timestamp EVENT: details`
- **Events**:
  - STARTED, EXITED
  - PROMPT_DETECTED, SEND_INPUT
  - IDLE_DETECTED, IDLE_NUDGE
  - DANGER_DETECTED
  - STATE_TRANSITION
  - ERROR_*

## Data Flow

### Normal Operation (Prompt Detection)

```
1. Claude outputs: "Continue? [y/n]"
   ↓
2. PTY read: "Continue? [y/n]"
   ↓
3. Output Monitor:
   - Writes to stdout
   - Appends to buffer
   - Updates timestamp
   ↓
4. Pattern Matcher:
   - Checks buffer against prompt regexes
   - Matches: '(?i)\[y/n\]'
   ↓
5. State Machine:
   - Check cooldown (OK)
   - Prepare response: "y\n"
   ↓
6. Input Injector:
   - Check rate limit (OK)
   - Write "y\n" to PTY
   - Update timestamps
   ↓
7. Logger: "SEND_INPUT: reason='Yes/No prompt' input='y\n'"
   ↓
8. Claude receives "y\n" and continues
```

### Idle Timeout Flow

```
1. Last output at T+0
   ↓
2. T+15s: Main loop checks timestamp
   - time.Since(lastOutput) = 15.1s
   - idle_timeout = 15s
   - Condition: 15.1s > 15s → TRUE
   ↓
3. State: RUNNING → IDLE_NUDGE
   ↓
4. Nudge sequence:
   - Send "\n"
   - Wait 1s
   - Send "y\n"
   - Wait 1s
   - Send "continue\n"
   - Wait 2s
   ↓
5. nudgeCount++
   ↓
6. State: IDLE_NUDGE → RUNNING
   ↓
7. If output resumes: Reset timer, continue
   If no output: Repeat up to 3 times
   If 3 attempts: RUNNING → MANUAL_MODE
```

### Danger Detection Flow

```
1. Claude outputs: "Next: rm -rf /"
   ↓
2. Output Monitor: Appends to buffer
   ↓
3. Danger Detector:
   - Checks buffer: matches 'rm\s+-rf\s+/'
   - Returns: DANGEROUS
   ↓
4. State Machine:
   - Immediate: ANY_STATE → MANUAL_MODE
   ↓
5. Logger: "DANGER_DETECTED: pattern='rm\s+-rf\s+/'"
   ↓
6. Print warning to stderr
   ↓
7. All future prompts: No auto-approval
   - Input Injector disabled
   - Passthrough mode only
```

## Concurrency Model

### Goroutines

```
┌─────────────────┐
│  Main Goroutine │  ← Event loop, state machine
└────────┬────────┘
         │ spawns
         ├──→ ┌─────────────────┐
         │    │ PTY Reader      │  ← Continuous read
         │    └─────────────────┘
         │
         ├──→ ┌─────────────────┐
         │    │ SIGWINCH Handler│  ← Terminal resize
         │    └─────────────────┘
         │
         └──→ ┌─────────────────┐
              │ Signal Handler  │  ← Ctrl+C, SIGTERM
              └─────────────────┘
```

### Communication

```
PTY Reader ──[outputCh]──→ Main Loop
           ──[errCh]─────→

Signal ────[sigCh]────────→ Main Loop

Main Loop ─[writes]───────→ PTY ─→ Claude
```

### Synchronization

- **stateMu** (RWMutex): Protects `state` field
  - Writers: setState()
  - Readers: getState()

- **lastOutputMu** (RWMutex): Protects `lastOutput` timestamp
  - Writers: updateLastOutput()
  - Readers: getTimeSinceLastOutput()

- **lastSendMu** (RWMutex): Protects `lastSend` timestamp
  - Writers: recordSend()
  - Readers: canSend()

- **CircularBuffer.mu** (Mutex): Protects buffer data
  - Writers: Write()
  - Readers: GetContent()

- **Channels**: Unbuffered communication
  - `outputCh` (buffered: 100): PTY output chunks
  - `errCh` (buffered: 1): Read errors
  - `sigCh` (buffered: 1): OS signals

## Configuration Flow

```
1. Load config file (YAML)
   ↓
2. Parse with gopkg.in/yaml.v3
   ↓
3. Apply defaults for missing values
   ↓
4. Compile regex patterns
   ↓
5. Create Wrapper instance
   ↓
6. Start PTY with command
   ↓
7. Run event loop
```

## Error Handling

### PTY Errors
- **Start failure**: Fatal error, exit
- **Read error**: Log, continue if not EOF
- **Write error**: Log, continue (may be transient)

### Regex Errors
- **Compile failure**: Fatal error at startup
- **Match failure**: Skip pattern, continue

### Process Errors
- **Exit**: Normal termination
- **Signal**: Caught, logged, handled

### State Errors
- **Invalid transition**: Logged, state corrected
- **Race condition**: Mutexes prevent

## Performance Profile

### Memory Usage
```
Circular Buffer:     4 KB
Output Channel:    400 KB  (100 × 4KB)
Regex Patterns:     20 KB  (15 compiled patterns)
Config:              1 KB
Goroutine Stacks:   16 KB  (4 × 4KB)
-----------------------------------
Total:            ~441 KB
```

### CPU Usage
```
Idle state:        < 1% CPU
Active:            < 5% CPU
Per chunk:
  - Read:          ~50 μs
  - Pattern match: ~100 μs (15 patterns)
  - Write:         ~50 μs
  Total:           ~200 μs / chunk
```

### Latency
```
Prompt → Detection:  < 1 ms
Detection → Send:    < 1 ms
Send → Claude:       < 1 ms
Total added:         < 10 ms
```

## Scalability

### Limits
- **Output rate**: Up to 100 chunks queued (400KB)
- **Pattern count**: 15 patterns efficient, 50+ may slow
- **Buffer size**: 4KB sufficient, larger = more memory
- **Child processes**: 1 per wrapper instance

### Bottlenecks
- **Regex matching**: O(n) per pattern, dominates CPU
- **Channel capacity**: 100 chunks before blocking
- **Mutex contention**: Minimal (read-heavy workload)

### Optimization Opportunities
1. **Regex prefiltering**: Check for common substrings first
2. **Buffer sizing**: Tune based on actual prompt lengths
3. **Channel sizing**: Increase if dealing with burst output
4. **Pattern caching**: Cache last N match results

## Security Considerations

### Threat Model
- **Malicious command injection**: Claude outputs harmful command
- **Resource exhaustion**: Large output, many prompts
- **Pattern bypass**: Obfuscated dangerous commands

### Mitigations
- **Danger detection**: Pattern-based blocking
- **Rate limiting**: Prevents approval storms
- **Bounded retries**: Prevents infinite loops
- **Resource limits**: Bounded buffers, channels
- **Manual override**: User can Ctrl+C anytime

### Limitations
- **Pattern-based only**: No semantic understanding
- **Context-blind**: Can't tell if `rm -rf /tmp/foo` is safe
- **Race windows**: Small gap between detection and action

## Testing Strategy

### Unit Tests (Recommended)
```
circular_buffer_test.go
  - Test Write/GetContent
  - Test overflow behavior
  - Test concurrency

pattern_matcher_test.go
  - Test prompt detection
  - Test danger detection
  - Test false positives/negatives

rate_limiter_test.go
  - Test global rate limit
  - Test per-rule cooldown
  - Test concurrent sends

state_machine_test.go
  - Test state transitions
  - Test invalid transitions
  - Test concurrent access
```

### Integration Tests
```
test-wrapper.sh:
  - Real prompt detection
  - Idle timeout behavior
  - Danger detection
  - Signal handling
```

### Load Tests
```
Scenario: Rapid prompts
  - 100 prompts in 10s
  - Verify: Rate limiting works
  - Verify: No approval spam

Scenario: Large output
  - 10MB output burst
  - Verify: No memory leak
  - Verify: Correct passthrough

Scenario: Long session
  - 1 hour runtime
  - Verify: No goroutine leak
  - Verify: No memory growth
```

## Deployment Considerations

### Single Binary
- **Advantages**: Easy distribution, no dependencies
- **Disadvantages**: Larger binary (~3.7MB)
- **Trade-off**: Worth it for simplicity

### Configuration
- **Embedded defaults**: Works without config file
- **External YAML**: Customizable per workflow
- **Override flags**: Command-line takes precedence

### Logging
- **Stderr only**: Keeps stdout clean for Claude
- **Structured**: Easy to parse
- **Timestamped**: Can reconstruct timeline

### Monitoring
- **Metrics**: None built-in (could add Prometheus)
- **Health checks**: Process running = healthy
- **Alerts**: Monitor log for DANGER_DETECTED, MANUAL_MODE

## Future Architecture Improvements

1. **Plugin system**: Load custom matchers dynamically
2. **Metrics export**: Prometheus endpoint
3. **Web UI**: View live state, logs, metrics
4. **Distributed mode**: Multiple wrappers coordinated
5. **Machine learning**: Learn from user corrections
6. **Context-aware detection**: Understand command semantics
7. **Audit trail**: Store decisions in database
8. **Hot reload**: Update config without restart

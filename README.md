# Auto-Approve Wrapper for Claude Code CLI

A robust PTY-based wrapper that automatically responds to Claude Code permission prompts while maintaining safety through dangerous command detection and idle timeout handling.

## Features

- **PTY-based**: Uses pseudo-terminal for proper interactive behavior
- **Smart prompt detection**: Regex-based pattern matching with cooldowns
- **Idle timeout handling**: Automatically nudges Claude when stuck
- **Safety first**: Detects and blocks dangerous commands
- **Structured logging**: Timestamped trace of all actions
- **Configurable**: YAML-based rule configuration
- **State machine**: Clear state transitions for debugging

## Installation

```bash
# Install dependencies
go get github.com/creack/pty
go get gopkg.in/yaml.v3

# Build
go build -o autoapprove autoapprove.go

# Or install to PATH
go install autoapprove.go
```

## Usage

### Basic Usage

```bash
# Use with default settings
./autoapprove -- claude --tools default

# With custom idle timeout
./autoapprove --idle 20s -- claude --tools default

# With custom config
./autoapprove --config rules.yaml -- claude --tools default

# Show default configuration
./autoapprove --show-defaults
```

### Example Commands

```bash
# Interactive chat with auto-approve
./autoapprove -- claude

# Code editing with longer idle timeout
./autoapprove --idle 30s -- claude --tools default "refactor the authentication module"

# Using custom rules
./autoapprove --config my-rules.yaml -- claude --tools default
```

## How It Works

### State Machine

The wrapper operates in four states:

1. **RUNNING**: Normal operation, monitoring output for prompts
2. **WAITING_FOR_PROMPT**: Detected a prompt, applying cooldown
3. **IDLE_NUDGE**: No output for idle_timeout, performing nudge sequence
4. **MANUAL_MODE**: Dangerous pattern detected or max retries exceeded

### Prompt Detection

The wrapper maintains a 4KB circular buffer of recent output and checks it against configured regex patterns. When a match is found:

1. Check if enough time has passed since last response (cooldown)
2. Check if minimum send interval has passed (rate limit)
3. Send the configured response
4. Reset idle timer

### Idle Timeout Handling

When no output is received for `idle_timeout`:

1. Send newline (`\n`)
2. Wait 1 second
3. Send `y\n`
4. Wait 1 second
5. Send `continue\n`
6. Wait 2 seconds
7. Return to RUNNING state

If idle timeout occurs `nudge_max_retries` times (default: 3), switch to MANUAL_MODE.

### Safety: Dangerous Command Detection

The wrapper scans all output for dangerous patterns:

- `rm -rf /` - Recursive root deletion
- `mkfs` - Format filesystem
- `dd if=` - Direct disk write
- `shutdown`, `reboot` - System control
- Fork bombs: `:(){:|:&};:`
- Pipe to shell: `curl|sh`, `wget|sh`
- Privileged file edits: `/etc/sudoers`
- Permission bombs: `chmod 777 /`
- Force push: `git push --force`
- Docker volume destruction

When detected:
1. Immediately switch to MANUAL_MODE
2. Print warning to stderr
3. Pass through all I/O without auto-approving
4. Log the detection with pattern details

### Rate Limiting

Two-level rate limiting prevents approval spam:

1. **Global minimum interval** (default: 500ms): Minimum time between ANY sends
2. **Per-rule cooldown** (configurable): Minimum time before same rule triggers again

## Configuration

### rules.yaml Format

```yaml
# Global settings
idle_timeout: 15s          # Time before idle nudge
min_send_interval: 500ms   # Rate limit between sends
nudge_initial_delay: 2s    # Delay before nudge starts
nudge_max_retries: 3       # Max nudges before manual mode

# Prompt rules
prompt_rules:
  - match_regex: '(?i)continue\s*\?'
    send: "y\n"
    description: "Continue prompt"
    cooldown: 2s
```

### Adding Custom Rules

```yaml
prompt_rules:
  # Custom approval pattern
  - match_regex: '(?i)execute\s+this\s+plan'
    send: "yes\n"
    description: "Plan execution approval"
    cooldown: 3s

  # Tool permission
  - match_regex: '(?i)allow\s+tool\s+usage'
    send: "y\n"
    description: "Tool usage permission"
    cooldown: 2s
```

## Behavior in Failure Modes

### Mode 1: Claude stops responding after quick approval

**Symptom**: You approve quickly, but Claude doesn't continue

**Cause**: Race condition or missed prompt

**Wrapper behavior**:
1. Detects no output for `idle_timeout` (default: 15s)
2. Enters IDLE_NUDGE state
3. Sends nudge sequence: `\n` → `y\n` → `continue\n`
4. Returns to RUNNING state
5. Repeats up to `nudge_max_retries` times

**Log output**:
```
[AUTOAPPROVE] IDLE_DETECTED: idle_time=15.2s threshold=15s
[AUTOAPPROVE] STATE_TRANSITION: RUNNING -> IDLE_NUDGE
[AUTOAPPROVE] IDLE_NUDGE: attempt=1/3
[AUTOAPPROVE] SEND_INPUT: reason='idle_nudge_1' input="\n"
[AUTOAPPROVE] SEND_INPUT: reason='idle_nudge_1' input="y\n"
[AUTOAPPROVE] SEND_INPUT: reason='idle_nudge_1' input="continue\n"
[AUTOAPPROVE] STATE_TRANSITION: IDLE_NUDGE -> RUNNING
```

### Mode 2: Claude appears stuck, then updates late

**Symptom**: No visible output, but after delay text appears in input area

**Cause**: Buffering, network latency, or slow processing

**Wrapper behavior**:
1. Waits for `idle_timeout` (doesn't trigger immediately)
2. If output resumes before timeout, resets idle timer
3. If timeout reached, performs safe nudge sequence
4. Nudges are benign (newline, yes) - won't disrupt if Claude was just slow

**Why it works**:
- Nudge sequence is designed to be safe even if Claude wasn't actually stuck
- Sending `\n` or `y\n` when Claude is processing is harmless
- Cooldowns prevent spam if output resumes

### Mode 3: Dangerous command detected

**Symptom**: Output contains `rm -rf /` or similar

**Wrapper behavior**:
1. Immediately switches to MANUAL_MODE
2. Prints warning to stderr
3. Stops all auto-approvals
4. Becomes transparent pass-through
5. User must manually respond

**Log output**:
```
[AUTOAPPROVE] DANGER_DETECTED: pattern='rm\s+-rf\s+/' matched in output
[AUTOAPPROVE] STATE_TRANSITION: RUNNING -> MANUAL_MODE

🚨 DANGER DETECTED: Auto-approve disabled. Switching to manual mode.
```

### Mode 4: Too many idle timeouts

**Symptom**: Repeated idle timeouts (3+ by default)

**Wrapper behavior**:
1. After `nudge_max_retries` attempts, switches to MANUAL_MODE
2. Prints warning
3. Stops auto-approvals
4. Prevents infinite nudge loops

**Log output**:
```
[AUTOAPPROVE] IDLE_TIMEOUT: max nudges reached, switching to manual mode

⚠️  AUTO-APPROVE DISABLED: Too many idle timeouts. Switching to manual mode.
```

### Mode 5: User interrupt (Ctrl+C)

**Symptom**: User presses Ctrl+C

**Wrapper behavior**:
1. Catches signal
2. Switches to MANUAL_MODE
3. Allows user to take control
4. Signal still propagates to child process

## Structured Logging

All wrapper actions are logged to stderr with timestamps:

```
[AUTOAPPROVE] 2026/02/06 10:15:30.123456 STARTED: command='claude' args=[--tools default]
[AUTOAPPROVE] 2026/02/06 10:15:32.234567 PROMPT_DETECTED: rule='Yes/No prompt'
[AUTOAPPROVE] 2026/02/06 10:15:32.234789 SEND_INPUT: reason='Yes/No prompt' input="y\n"
[AUTOAPPROVE] 2026/02/06 10:15:45.345678 IDLE_DETECTED: idle_time=15.1s threshold=15s
[AUTOAPPROVE] 2026/02/06 10:15:45.345890 STATE_TRANSITION: RUNNING -> IDLE_NUDGE
```

Log event types:
- `STARTED`: Command launched
- `STATE_TRANSITION`: State change
- `PROMPT_DETECTED`: Matched prompt pattern
- `SEND_INPUT`: Input sent to PTY
- `IDLE_DETECTED`: Idle timeout triggered
- `IDLE_NUDGE`: Nudge sequence started
- `DANGER_DETECTED`: Dangerous pattern found
- `ERROR_*`: Various errors
- `EXITED`: Command finished

## Debugging

### Enable verbose output

Structured logging is always enabled on stderr. To debug:

```bash
# Redirect logs to file
./autoapprove -- claude 2> autoapprove.log

# Watch logs in real-time
./autoapprove -- claude 2>&1 | tee session.log
```

### Test prompt detection

```bash
# Show default config including regex patterns
./autoapprove --show-defaults

# Test with a simple interactive command
./autoapprove -- bash -c 'read -p "Continue? [y/n] " answer && echo $answer'
```

### Adjust timeouts

```bash
# Longer idle timeout for slow operations
./autoapprove --idle 30s -- claude

# Custom config with all parameters
cat > test-rules.yaml <<EOF
idle_timeout: 10s
min_send_interval: 200ms
nudge_max_retries: 5
prompt_rules:
  - match_regex: '(?i)continue'
    send: "yes\n"
    description: "Test rule"
    cooldown: 1s
EOF

./autoapprove --config test-rules.yaml -- claude
```

## Safety Guarantees

1. **Never auto-approves destructive operations**: Dangerous commands trigger manual mode
2. **Rate limited**: Cannot spam approvals faster than configured intervals
3. **Bounded retries**: Switches to manual mode after max nudge attempts
4. **User override**: Ctrl+C always switches to manual mode
5. **Transparent in manual mode**: Becomes simple pass-through
6. **Logged actions**: All decisions are timestamped and logged

## Limitations

- Prompt detection depends on regex accuracy
- Cannot distinguish between similar-looking prompts without context
- Idle timeout may trigger on legitimately long operations
- Dangerous command detection is pattern-based (can have false positives/negatives)
- No machine learning or context awareness

## Troubleshooting

### Wrapper isn't responding to prompts

1. Check log output for `PROMPT_DETECTED` events
2. Verify your regex patterns with `--show-defaults`
3. Test regex against actual prompt text
4. Check if cooldown is too long

### Too many false idle detections

1. Increase `--idle` timeout
2. Check if Claude operation is legitimately slow
3. Reduce `nudge_max_retries` to fail faster

### Dangerous command false positive

1. Review pattern list in `autoapprove.go`
2. If pattern is too broad, create custom version
3. Consider if the command really is dangerous

### Wrapper becomes unresponsive

1. Check logs for state transitions
2. May be in MANUAL_MODE - check for danger/interrupt messages
3. Verify child process is still running

## License

MIT - Use at your own risk. Always review what Claude is doing, especially with auto-approve enabled.

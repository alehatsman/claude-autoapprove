# Quick Start Guide

## Installation

```bash
# 1. Download dependencies
go mod download

# 2. Build
go build -o autoapprove autoapprove.go

# 3. (Optional) Install to PATH
go install autoapprove.go
```

## Basic Usage

### Wrap Claude Code CLI

```bash
# Default settings (15s idle timeout)
./autoapprove -- claude

# With custom idle timeout
./autoapprove --idle 20s -- claude

# With specific Claude command
./autoapprove -- claude --tools default "refactor the auth module"
```

### With Custom Configuration

```bash
# Use custom rules
./autoapprove --config rules.yaml -- claude

# View default configuration
./autoapprove --show-defaults > my-rules.yaml
# Edit my-rules.yaml, then:
./autoapprove --config my-rules.yaml -- claude
```

## What It Does

1. **Auto-approves prompts** matching configured patterns:
   - `[y/n]` → sends `y`
   - `Continue?` → sends `y`
   - `Press Enter` → sends newline

2. **Handles stuck states** with idle timeout:
   - After 15s of no output, sends nudge sequence
   - Nudges: `\n` → `y\n` → `continue\n`
   - Up to 3 nudge attempts

3. **Protects against dangerous commands**:
   - Detects: `rm -rf /`, `mkfs`, `shutdown`, etc.
   - Switches to manual mode (no auto-approval)
   - Prints warning to stderr

4. **Logs everything** to stderr:
   - Prompt detections
   - Input sent
   - State changes
   - Idle timeouts

## Configuration File Format

Create `rules.yaml`:

```yaml
idle_timeout: 15s
min_send_interval: 500ms
nudge_max_retries: 3

prompt_rules:
  - match_regex: '(?i)continue\?'
    send: "y\n"
    description: "Continue prompt"
    cooldown: 2s

  - match_regex: '(?i)\[y/n\]'
    send: "y\n"
    description: "Yes/No prompt"
    cooldown: 2s
```

## Testing

```bash
# Run test suite
./test-wrapper.sh

# Manual tests
./autoapprove -- bash -c 'read -p "Continue? [y/n] " x; echo "Got: $x"'
./autoapprove --idle 5s -- bash -c 'echo "Wait..."; sleep 10; echo "Done"'
./autoapprove -- bash -c 'echo "Next: rm -rf /"; sleep 5'
```

## Troubleshooting

### Wrapper isn't responding to prompts
- Check logs: `./autoapprove -- claude 2> debug.log`
- Verify prompt pattern: `./autoapprove --show-defaults`
- Increase verbosity by watching stderr

### Too many false idle detections
- Increase timeout: `./autoapprove --idle 30s -- claude`
- Check if Claude operations are legitimately slow

### Dangerous command false positive
- Review warning in stderr
- Consider if command really is dangerous
- If false positive, you'll need to manually approve

## Common Commands

```bash
# Production use
./autoapprove --idle 20s --config rules.yaml -- claude --tools default

# Debugging
./autoapprove -- claude 2>&1 | tee session.log

# Quick approval for known-safe task
./autoapprove --idle 10s -- claude "add type hints to utils.py"

# Disable by pressing Ctrl+C (switches to manual mode)
```

## Logs Example

```
[AUTOAPPROVE] 2026/02/06 10:15:30 STARTED: command='claude' args=[]
[AUTOAPPROVE] 2026/02/06 10:15:35 PROMPT_DETECTED: rule='Yes/No prompt'
[AUTOAPPROVE] 2026/02/06 10:15:35 SEND_INPUT: reason='Yes/No prompt' input="y\n"
[AUTOAPPROVE] 2026/02/06 10:15:50 IDLE_DETECTED: idle_time=15.1s threshold=15s
[AUTOAPPROVE] 2026/02/06 10:15:50 STATE_TRANSITION: RUNNING -> IDLE_NUDGE
[AUTOAPPROVE] 2026/02/06 10:15:50 IDLE_NUDGE: attempt=1/3
[AUTOAPPROVE] 2026/02/06 10:15:50 SEND_INPUT: reason='idle_nudge_1' input="\n"
```

## Safety Features

- ✅ Rate limiting (500ms between sends)
- ✅ Per-rule cooldowns (1-2s)
- ✅ Dangerous pattern detection
- ✅ Max retry limits (3 nudges)
- ✅ User interrupt (Ctrl+C)
- ✅ Structured logging

## Next Steps

1. Test with simple commands first
2. Monitor logs to understand behavior
3. Customize `rules.yaml` for your patterns
4. Adjust `--idle` timeout based on your workflow
5. Add your own prompt patterns as needed

## Help

```bash
./autoapprove --help
```

For issues: Review logs, check IMPLEMENTATION.md for details, or adjust configuration.

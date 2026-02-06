# Usage Examples

Real-world scenarios and command patterns for using the auto-approve wrapper.

## Basic Scenarios

### 1. Simple Interactive Session

```bash
# Start Claude with auto-approve enabled
./autoapprove -- claude

# Claude will automatically approve:
# - File read permissions
# - Tool usage permissions
# - Continue/proceed prompts
# - Yes/no questions
```

### 2. Specific Task with Timeout

```bash
# Refactoring task with 30s idle timeout
./autoapprove --idle 30s -- claude "refactor the authentication module to use JWT"

# Why 30s? Refactoring may have longer thinking pauses
```

### 3. Batch Operations

```bash
# Process multiple files
./autoapprove --idle 20s -- claude "add type hints to all Python files in src/"

# Multiple API calls may need longer idle timeout
```

### 4. With Custom Rules

```bash
# Use project-specific approval rules
./autoapprove --config project-rules.yaml -- claude

# project-rules.yaml might include custom patterns:
# - match_regex: '(?i)deploy\s+to\s+staging'
#   send: "yes\n"
#   description: "Staging deployment approval"
```

## Advanced Scenarios

### 5. CI/CD Integration

```bash
#!/bin/bash
# ci-task.sh - Run Claude in CI pipeline

set -e

# Stricter timeout for CI (fail fast)
./autoapprove --idle 10s --config ci-rules.yaml -- claude \
  "run all tests and fix any failures" 2>&1 | tee claude-ci.log

# Check exit code
if [ $? -eq 0 ]; then
  echo "Claude task completed successfully"
else
  echo "Claude task failed"
  exit 1
fi
```

### 6. Logging and Debugging

```bash
# Separate stdout and stderr for analysis
./autoapprove -- claude "optimize database queries" \
  1> claude-output.txt \
  2> wrapper-log.txt

# Watch logs in real-time
tail -f wrapper-log.txt

# Later analysis
grep "PROMPT_DETECTED" wrapper-log.txt
grep "IDLE_DETECTED" wrapper-log.txt
grep "DANGER_DETECTED" wrapper-log.txt
```

### 7. Safe Destructive Operations

```bash
# The wrapper will auto-block dangerous commands
./autoapprove -- claude "clean up old log files in /tmp"

# If Claude suggests: rm -rf /tmp/*
# Wrapper will:
# 1. Detect dangerous pattern
# 2. Switch to MANUAL_MODE
# 3. Print warning
# 4. Wait for manual approval
```

### 8. Multiple Wrapper Instances

```bash
# Terminal 1: Frontend work
./autoapprove --idle 15s -- claude "update React components"

# Terminal 2: Backend work (different project)
cd ../backend
./autoapprove --idle 20s -- claude "add new API endpoint"

# Each instance is independent with its own state
```

## Custom Configuration Examples

### 9. Conservative Configuration

```yaml
# conservative-rules.yaml
# Longer timeouts, fewer retries, manual mode faster

idle_timeout: 30s
min_send_interval: 1s
nudge_max_retries: 2

prompt_rules:
  - match_regex: '(?i)\[y/n\]'
    send: "y\n"
    description: "Yes/No prompt"
    cooldown: 5s
```

```bash
./autoapprove --config conservative-rules.yaml -- claude
```

### 10. Aggressive Configuration

```yaml
# aggressive-rules.yaml
# Shorter timeouts, more retries, faster responses

idle_timeout: 5s
min_send_interval: 200ms
nudge_max_retries: 5

prompt_rules:
  - match_regex: '(?i)(continue|proceed|approve|allow|permit)'
    send: "y\n"
    description: "Any approval request"
    cooldown: 1s
```

```bash
./autoapprove --config aggressive-rules.yaml -- claude
```

### 11. Project-Specific Patterns

```yaml
# project-rules.yaml
# Custom patterns for your workflow

idle_timeout: 15s
min_send_interval: 500ms
nudge_max_retries: 3

prompt_rules:
  # Standard prompts
  - match_regex: '(?i)\[y/n\]'
    send: "y\n"
    description: "Yes/No"
    cooldown: 2s

  # Custom: Your project uses "PROCEED?"
  - match_regex: 'PROCEED\?'
    send: "YES\n"
    description: "Project proceed prompt"
    cooldown: 2s

  # Custom: Tool approval in your workflow
  - match_regex: 'Allow\s+(\w+)\s+tool'
    send: "allow\n"
    description: "Tool permission"
    cooldown: 1s

  # Custom: Deployment confirmation
  - match_regex: 'Deploy\s+to\s+(\w+)\s+environment'
    send: "confirm\n"
    description: "Deployment confirmation"
    cooldown: 5s
```

## Monitoring and Analysis

### 12. Real-Time Monitoring

```bash
# In one terminal: Run Claude
./autoapprove --idle 15s -- claude "large refactoring task" \
  2> wrapper.log

# In another terminal: Watch logs
watch -n 1 'tail -20 wrapper.log | grep -E "(PROMPT_DETECTED|SEND_INPUT|IDLE|DANGER)"'
```

### 13. Post-Session Analysis

```bash
# Run a session
./autoapprove -- claude "complex task" 2> session-$(date +%Y%m%d-%H%M%S).log

# Analyze later
LOG_FILE="session-20260206-102030.log"

# Count prompt approvals
grep -c "PROMPT_DETECTED" $LOG_FILE

# Find idle timeouts
grep "IDLE_DETECTED" $LOG_FILE

# Check for dangers
grep "DANGER_DETECTED" $LOG_FILE

# Timeline of events
grep -E "(STATE_TRANSITION|SEND_INPUT)" $LOG_FILE
```

### 14. Performance Metrics

```bash
# Extract timing data
awk '/SEND_INPUT/ {print $2, $3}' wrapper.log | while read date time; do
  echo "$date $time"
done > approval-times.txt

# Count approvals per minute
awk -F: '/SEND_INPUT/ {print $1":"$2}' wrapper.log | uniq -c

# Average idle time when detected
grep "IDLE_DETECTED" wrapper.log | \
  sed 's/.*idle_time=\([0-9.]*\)s.*/\1/' | \
  awk '{sum+=$1; count++} END {print "Avg idle:", sum/count, "s"}'
```

## Error Handling

### 15. Graceful Degradation

```bash
# Wrapper with fallback
./autoapprove --idle 15s -- claude "task" 2> wrapper.log
WRAPPER_EXIT=$?

if [ $WRAPPER_EXIT -ne 0 ]; then
  echo "Auto-approve failed, trying manual mode..."
  claude "task"
fi
```

### 16. Timeout Protection

```bash
# Prevent infinite runs with system timeout
timeout 10m ./autoapprove --idle 20s -- claude "bounded task"

# Exit codes:
# 0 = success
# 124 = timeout reached
# other = error
```

### 17. Signal Handling

```bash
# Start wrapper in background
./autoapprove -- claude "long task" &
WRAPPER_PID=$!

# Later: Switch to manual mode
kill -INT $WRAPPER_PID  # Sends SIGINT (like Ctrl+C)

# Wrapper will:
# 1. Catch signal
# 2. Switch to MANUAL_MODE
# 3. Continue running in pass-through mode
```

## Testing and Development

### 18. Test Your Patterns

```bash
# Create test script
cat > test-prompt.sh <<'EOF'
#!/bin/bash
echo "Starting task..."
sleep 1
echo "Do you want to continue? [y/n]"
read -t 5 response
echo "Received: $response"
EOF

chmod +x test-prompt.sh

# Test wrapper
./autoapprove -- ./test-prompt.sh
```

### 19. Debug Regex Patterns

```bash
# Test if your regex matches
echo "Continue? [y/n]" | grep -P '(?i)\[y/n\]' && echo "Match!"

# Test with autoapprove
./autoapprove --show-defaults | grep -A 2 "match_regex"

# Add verbose test pattern
cat > test-rules.yaml <<EOF
prompt_rules:
  - match_regex: 'TEST_PROMPT'
    send: "test_response\n"
    description: "Test pattern"
    cooldown: 1s
EOF

# Test it
echo "Waiting for TEST_PROMPT" | ./autoapprove --config test-rules.yaml -- cat
```

### 20. Dry Run Mode (Manual)

```bash
# Log what would be approved without actually running
# (Manual simulation)

# 1. Capture Claude output
claude "task" > capture.txt 2>&1

# 2. Test patterns against capture
while IFS= read -r line; do
  if echo "$line" | grep -qP '(?i)\[y/n\]'; then
    echo "Would approve: $line"
  fi
done < capture.txt
```

## Production Patterns

### 21. Automated Code Reviews

```bash
# Review PR automatically
./autoapprove --idle 30s --config review-rules.yaml -- \
  claude "review PR #123 and suggest improvements"
```

### 22. Automated Testing

```bash
# Run tests and auto-approve test execution
./autoapprove --idle 10s -- \
  claude "run all unit tests and fix any failures"
```

### 23. Documentation Generation

```bash
# Generate docs with auto-approval
./autoapprove --idle 20s -- \
  claude "update API documentation for all endpoints"
```

### 24. Batch Refactoring

```bash
# Refactor multiple files
./autoapprove --idle 25s -- \
  claude "refactor all services to use async/await pattern"
```

### 25. Scheduled Tasks

```bash
# Cron job: Daily code cleanup
# /etc/cron.daily/claude-cleanup.sh

#!/bin/bash
cd /path/to/project
./autoapprove --idle 15s --config cleanup-rules.yaml -- \
  claude "run linter and fix all auto-fixable issues" \
  2>> /var/log/claude-cleanup.log

# Commit changes if any
if [ -n "$(git status --porcelain)" ]; then
  git add -A
  git commit -m "Automated cleanup by Claude"
fi
```

## Troubleshooting Scenarios

### 26. Wrapper Not Responding

```bash
# Debug: Enable verbose logging
./autoapprove -- claude 2>&1 | tee debug.log

# Check for:
# - PROMPT_DETECTED events
# - STATE_TRANSITION events
# - Any ERROR_ events

# If no PROMPT_DETECTED, pattern might not match
./autoapprove --show-defaults > current-rules.yaml
# Review patterns
```

### 27. Too Many False Positives

```bash
# If wrapper approves too eagerly:
# 1. Increase cooldowns in rules.yaml
# 2. Make patterns more specific
# 3. Increase min_send_interval

cat > strict-rules.yaml <<EOF
min_send_interval: 1s  # Was 500ms

prompt_rules:
  - match_regex: 'Continue\?\s+\[y/n\]$'  # More specific ($ = end of line)
    send: "y\n"
    cooldown: 3s  # Longer cooldown
EOF

./autoapprove --config strict-rules.yaml -- claude
```

### 28. Recovery from Manual Mode

```bash
# If wrapper switches to manual mode unexpectedly:

# Check logs for reason
grep "STATE_TRANSITION.*MANUAL" wrapper.log

# Common reasons:
# - DANGER_DETECTED: Review command, approve manually if safe
# - IDLE_TIMEOUT: Increase --idle timeout
# - User interrupt: Resume by not pressing Ctrl+C

# Solution: Restart with appropriate settings
./autoapprove --idle 30s -- claude  # Longer timeout
```

## Best Practices

1. **Start Conservative**: Use default settings first, tune later
2. **Monitor Initially**: Watch logs for first few runs
3. **Test Patterns**: Verify regex matches before production use
4. **Document Custom Rules**: Comment your rules.yaml
5. **Version Control**: Track rules.yaml in git
6. **Separate Configs**: Different rules for different workflows
7. **Log Everything**: Keep logs for debugging
8. **Review Periodically**: Check what was auto-approved
9. **Update Patterns**: Add new patterns as Claude evolves
10. **Stay Safe**: When in doubt, use manual mode

## Quick Reference

```bash
# Most common usage
./autoapprove -- claude

# Custom timeout
./autoapprove --idle 20s -- claude

# Custom rules
./autoapprove --config rules.yaml -- claude

# View defaults
./autoapprove --show-defaults

# Debug
./autoapprove -- claude 2>&1 | tee session.log

# Test
./test-wrapper.sh
```

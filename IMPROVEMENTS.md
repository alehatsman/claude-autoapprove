# Claude Wrapper - Robustness Improvements

## Architecture Changes to Never Get Stuck

### 1. **Watchdog Timer** (HIGH PRIORITY)
Independent thread that monitors overall health:
- If no activity (no approvals, no input) for 10+ seconds → force action
- Sends Enter as last resort
- Logs warning about forced action
- **Never lets system sit idle indefinitely**

```python
class WatchdogTimer:
    def __init__(self):
        self.last_activity = time.time()
        self.timeout = 10.0  # seconds

    def reset(self):
        self.last_activity = time.time()

    def check(self):
        if time.time() - self.last_activity > self.timeout:
            # Force action: send Enter
            self.force_approval()
```

### 2. **Progressive Response Strategy** (HIGH PRIORITY)
Multiple fallback attempts when stuck:
- 2.5s: Send Enter (safest, works for most prompts)
- 5.0s: Send 'y' + Enter (for text y/n prompts)
- 7.5s: Send '1' + Enter (for numbered menus)
- 10s: Send multiple Enters (force through any state)
- **Escalating responses ensure we eventually unstick**

### 3. **Adaptive Detection Thresholds** (HIGH PRIORITY)
Lower score requirements the longer we're idle:
- 0-2.5s idle: score >= 3 required (strict)
- 2.5-5s idle: score >= 2 required (lenient)
- 5-10s idle: score >= 1 required (very lenient)
- 10s+ idle: score >= 0 (accept anything, force action)
- **Catches edge cases that almost look like prompts**

### 4. **State Machine with Timeouts** (HIGH PRIORITY)
Clear states with mandatory timeouts:
```
IDLE → PROMPT_DETECTED → COUNTDOWN → APPROVING → IDLE
  ↓         ↓                ↓           ↓
 10s       5s              2s          3s      (max time in state)
  ↓         ↓                ↓           ↓
FORCE    FORCE           FORCE       FORCE
```
- Each state has maximum duration
- If exceeded, force transition to next state
- **Never stuck in one state indefinitely**

### 5. **Buffer Change Detection** (MEDIUM PRIORITY)
Track what's new vs old:
- Keep history of last 5 buffers with timestamps
- Use diff to identify truly new content
- Only trigger detection on NEW text, not old text
- **Prevents false positives from buffer pollution**

### 6. **Self-Healing** (MEDIUM PRIORITY)
Auto-reset when stuck detected:
- If no approvals for 15+ seconds, reset all state
- Clear flags, hashes, buffers
- Send Enter to try to unstick
- Log self-healing action
- **Automatic recovery without user intervention**

### 7. **Escape Hatch** (MEDIUM PRIORITY)
User override key (Ctrl+Shift+A or Ctrl+E):
- Bypasses all checks
- Immediately sends Enter
- User can manually unstick when auto-approve fails
- **User has direct control as backup**

### 8. **Health Status in Status Bar** (LOW PRIORITY)
Show wrapper health:
- `[PID 12345] Healthy - Ready (auto-approve ON)`
- `[PID 12345] Degraded - Stuck? (auto-approve ON)` (if idle > 15s)
- `[PID 12345] Self-healing... (auto-approve ON)` (during recovery)
- **User can see if wrapper is working properly**

### 9. **Prompt History & Learning** (LOW PRIORITY)
Remember what was manually approved:
- When user manually approves (countdown cancelled), remember the prompt
- Add to custom patterns in config
- Improve detection over time
- **Adaptive learning from user behavior**

### 10. **Multiple Detection Methods** (LOW PRIORITY)
Don't rely only on text patterns:
- Primary: Text pattern matching
- Secondary: Check if process is waiting for input (using pty state)
- Tertiary: Cursor position detection
- **Multi-layered detection is more robust**

---

## Implementation Priority

### Phase 1 (Critical - Implement Now):
1. ✅ Watchdog Timer
2. ✅ Progressive Response Strategy
3. ✅ Adaptive Thresholds
4. ✅ State Machine with Timeouts

### Phase 2 (Important):
5. Buffer Change Detection
6. Self-Healing
7. Escape Hatch

### Phase 3 (Nice to Have):
8. Health Status
9. Prompt Learning
10. Multiple Detection Methods

---

## Key Principle: **Fail-Safe, Not Fail-Deadly**

The wrapper should:
- ✅ Try smart detection first
- ✅ Fall back to aggressive detection
- ✅ Eventually force action if stuck
- ✅ Never sit idle indefinitely
- ✅ Always give user manual override
- ✅ Log all recovery actions for debugging

**Goal: Zero stuck states, even in worst-case scenarios**

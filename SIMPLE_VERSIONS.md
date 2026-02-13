# Simple Single-File Implementations

This directory contains two single-file implementations of Claude auto-approve functionality.

## Python Version: `claude-autoapprove-simple.py`

**Features:**
- ✅ Single file, ~388 lines
- ✅ Zero external dependencies (stdlib only)
- ✅ Prompt detection with scoring system
- ✅ 3-second countdown with visual feedback
- ✅ Ctrl+A toggle for enable/disable
- ✅ Status bar with approval counter
- ✅ Handles both "yes" and Enter approvals

**Usage:**
```bash
./claude-autoapprove-simple.py [claude args...]
```

**Requirements:**
- Python 3.6+

## Go Version: `claude-autoapprove-simple.go`

**Features:**
- ✅ Single file, ~450 lines
- ✅ Concurrent I/O with goroutines and channels
- ✅ Same prompt detection logic as Python version
- ✅ 3-second countdown with visual feedback
- ✅ Ctrl+A toggle for enable/disable
- ✅ Status bar with approval counter
- ✅ Handles both "yes" and Enter approvals

**Usage:**
```bash
# Build first
./build-go.sh

# Then run
./claude-autoapprove-go [claude args...]
```

**Requirements:**
- Go 1.21+
- Dependencies: `github.com/creack/pty`, `golang.org/x/term`

## Feature Comparison

| Feature | Python | Go |
|---------|--------|-----|
| Single file | ✅ | ✅ |
| Stdlib only | ✅ | ❌ (needs pty pkg) |
| Lines of code | ~388 | ~450 |
| Concurrency model | Threading | Goroutines |
| PTY handling | `pty` module | `creack/pty` |
| Prompt detection | Regex + scoring | Regex + scoring |
| Countdown | Thread-based | Goroutine-based |
| Toggle support | ✅ Ctrl+A | ✅ Ctrl+A |
| Status bar | ✅ | ✅ |
| Buffer limit | 4KB | 4KB |

## Key Differences

### Python Version
- **Pros:**
  - No external dependencies
  - Easier to deploy (just copy the file)
  - More familiar to Python developers
  - Simpler threading model

- **Cons:**
  - GIL can impact performance
  - Threading overhead

### Go Version
- **Pros:**
  - Better concurrent I/O handling
  - No GIL limitations
  - Faster execution
  - Static binary (after build)
  - Goroutines more efficient than threads

- **Cons:**
  - Requires external dependencies
  - Need to build before running
  - Larger binary size

## Implementation Notes

### Prompt Detection
Both versions use identical detection logic:
- Score-based system (threshold: 3 points)
- Multiple pattern types:
  - Permission prompts (2 pts)
  - File operations (2 pts)
  - UI elements (1 pt each)
  - Yes/No menus (3 pts)
- Code block safety filter

### I/O Architecture

**Python:**
```python
select.select([sys.stdin, master_fd], [], [], timeout)
```
Uses `select()` system call to multiplex I/O.

**Go:**
```go
go func() { /* read stdin */ }()
go func() { /* read ptmx */ }()
select {
  case data := <-stdinChan: ...
  case data := <-ptmxChan: ...
}
```
Uses goroutines + channels for concurrent I/O.

### Countdown Mechanism

**Python:**
```python
threading.Thread(target=countdown_and_approve)
countdown_cancelled = threading.Event()
countdown_approve_now = threading.Event()
```

**Go:**
```go
go func() { countdownAndApprove() }()
countdownCancelled chan struct{}
countdownApproveNow chan struct{}
```

Both use similar patterns adapted to their language idioms.

## Which Should You Use?

**Use Python version if:**
- You want zero dependencies
- You're on a system with Python pre-installed
- You prefer Python's simplicity
- You want easy deployment (just copy the file)

**Use Go version if:**
- You want better performance
- You prefer Go's concurrency model
- You want a static binary
- You're comfortable with Go tooling

**Use neither (use the package) if:**
- You need production features
- You want configuration support
- You need logging/debugging
- You want idle detection
- You need rate limiting

## Building & Running

### Python
```bash
# Make executable
chmod +x claude-autoapprove-simple.py

# Run directly
./claude-autoapprove-simple.py

# Or with python
python3 claude-autoapprove-simple.py
```

### Go
```bash
# Build
./build-go.sh

# Run
./claude-autoapprove-go

# Or build manually
go mod download
go build -o claude-autoapprove-go claude-autoapprove-simple.go
```

## Testing

Both versions support the same test workflow:
1. Run the wrapper
2. Trigger a permission prompt (e.g., ask Claude to create a file)
3. Observe 3-second countdown
4. Press Enter to approve immediately
5. Press any other key to cancel
6. Press Ctrl+A to toggle auto-approve

## Known Limitations

Both versions share these limitations:
- No rate limiting
- No idle detection
- No logging/debugging
- Fixed 3-second countdown
- Fixed detection thresholds
- No configuration file support

For production use with these features, use the full package version in `src/claude_autoapprove/`.

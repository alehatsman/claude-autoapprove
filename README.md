# Claude Auto-Approve

[![Go Version](https://img.shields.io/badge/go-1.21%2B-blue.svg)](https://golang.org/dl/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)

A lightweight Go wrapper for Claude Code that automatically approves permission prompts after a configurable countdown, with a clean status bar interface.

## Features

- **Auto-approve permissions** with 3-second countdown timer
- **Smart prompt detection** using multi-factor scoring system
- **Status bar** showing countdown and approval count
- **Toggle auto-approve** on/off with Ctrl+A
- **Instant approval** with Enter during countdown
- **Cancel countdown** with any other key
- **PTY-based** for full terminal compatibility
- **Zero configuration** - works out of the box

## Installation

### Prerequisites

- Go 1.21 or later
- Claude Code CLI installed
- Unix-like system (Linux, macOS)

### Quick Install

```bash
# Clone the repository
git clone https://github.com/yourusername/claude-autoapprove.git
cd claude-autoapprove

# Build and install to /usr/local/bin
make install
```

### Build Only

```bash
# Just build the binary
make build

# Or use the build script
./build-go.sh

# Or build directly
go build -o claude-autoapprove main.go
```

## Quick Start

```bash
# Run with default settings
./claude-autoapprove

# Pass arguments to Claude Code
./claude-autoapprove --help
./claude-autoapprove 'review this code'

# Run from PATH (if installed)
claude-autoapprove
```

## Keyboard Controls

- **Enter** during countdown: Approve immediately
- **Any key** during countdown: Cancel auto-approve
- **Ctrl+A**: Toggle auto-approve on/off
- **Esc**: Cancel (passed to Claude)

## How It Works

1. **Terminal Setup**: Creates a pseudo-terminal (PTY) for the Claude process
2. **Prompt Detection**: Monitors output for permission prompts using multi-factor scoring
3. **Countdown**: Shows 3-second countdown in status bar at the bottom
4. **User Control**: Allows instant approval (Enter), cancellation (any key), or toggle (Ctrl+A)
5. **Auto-Execute**: Sends "yes" + Enter or just Enter based on prompt type

### Detection Algorithm

The wrapper scores each output chunk based on indicators:

**Strong indicators (score +2-3):**
- "Permission rule"
- "Do you want to proceed?" / "Would you like to proceed?"
- File operation prompts (create/edit/delete/modify/write)
- Yes/No button patterns ("1. Yes", "2. No")

**Moderate indicators (score +1):**
- "Esc to cancel"
- "Tab to amend"
- "Enter to approve/confirm"
- "(y/n)" prompt pattern

**Threshold:** Score ≥ 3 to trigger auto-approve

**Safety:** Code blocks (```) automatically zero the score

## Status Bar

The status bar at the bottom shows:

```
Ready (auto-approve ON) [Ctrl+A=toggle]
⏱  Auto-approving in 3s... (Enter=now, any key=cancel, Ctrl+A=off)
✓ Auto-approved (#1)
✗ Auto-approve DISABLED
```

## Project Structure

```
.
├── main.go              # Main source file (~520 lines)
├── go.mod               # Go module definition
├── go.sum               # Go dependencies
├── build-go.sh          # Build script
├── claude-autoapprove   # Compiled binary
├── README.md            # This file
├── LICENSE              # MIT License
├── SIMPLE_VERSIONS.md   # Version history
├── docs/                # Documentation
└── examples/            # Example usage
```

## Architecture

The code is organized into focused components within `main.go`:

- **Prompt Detection** (`isPrompt`, `needsYes`) - Pattern matching and scoring
- **ClaudeWrapper** struct - Main state management
- **Terminal Management** - PTY setup, sizing, scrolling regions
- **Status Bar** - Drawing and clearing status messages
- **Countdown Logic** - Goroutine-based countdown with cancellation
- **I/O Handling** - Multiplexed stdin/stdout with the Claude process
- **User Input** - Keyboard control (toggle, cancel, instant approve)

## Safety Features

- **Smart Detection**: Requires multiple indicators (score ≥ 3) to avoid false positives
- **Code Block Protection**: Never auto-approves within code blocks (```)
- **User Control**: Easy toggle (Ctrl+A) and cancellation (any key)
- **Visual Feedback**: Clear status bar showing countdown and state
- **Configurable Countdown**: 3-second delay gives time to cancel

## Dependencies

- [`github.com/creack/pty`](https://github.com/creack/pty) - PTY interface for Go
- [`golang.org/x/term`](https://golang.org/x/term) - Terminal control

## Development

### Makefile Targets

```bash
# Build the binary
make build

# Build and install to /usr/local/bin
make install

# Clean build artifacts
make clean
```

### Manual Building

```bash
# Build for current platform
go build -o claude-autoapprove main.go

# Build with optimizations
go build -ldflags="-s -w" -o claude-autoapprove main.go

# Cross-compile for Linux
GOOS=linux GOARCH=amd64 go build -o claude-autoapprove-linux main.go

# Cross-compile for macOS
GOOS=darwin GOARCH=amd64 go build -o claude-autoapprove-macos main.go
```

### Code Overview

Key types and functions:

```go
type ClaudeWrapper struct {
    autoApprove         bool              // Current auto-approve state
    ptmx                *os.File          // PTY master
    buffer              string            // Output buffer for detection
    countdownRunning    bool              // Countdown state
    approvalCount       int               // Total approvals
    // ... terminal state, channels, etc.
}

func isPrompt(text string) (bool, int)           // Detect permission prompts
func needsYes(text string) bool                  // Check if "yes" text needed
func (w *ClaudeWrapper) run(args []string) int   // Main entry point
```

## Requirements

- **Go**: 1.21 or later
- **OS**: Unix-like system (Linux, macOS) - uses PTY
- **Claude Code**: CLI must be in PATH
- **Terminal**: ANSI escape code support

## Known Limitations

- Unix/macOS only (requires PTY support)
- Assumes Claude Code binary is named `claude` and in PATH
- Fixed 3-second countdown (not configurable without rebuilding)
- Status bar always enabled

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Acknowledgments

Built with Claude Code and developed collaboratively with Claude AI.

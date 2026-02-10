# Claude Auto-Approve Wrapper

A Python-based wrapper for Claude Code CLI that automatically approves permission prompts with configurable countdown and safety features.

## Features

- **Auto-approve with countdown**: Automatically approves permission prompts after a configurable delay
- **Cancellable**: Press any key to cancel auto-approve, or Enter to approve immediately
- **Toggle on/off**: Press Ctrl+A to toggle auto-approve on/off dynamically
- **Smart detection**: Uses scoring system to accurately detect permission prompts
- **Rate limiting**: Prevents infinite loops with duplicate prompt detection
- **Idle detection**: Automatically handles stuck prompts after inactivity
- **Status bar**: Shows current state and approval counter
- **Terminal-aware**: Handles terminal resizing and maintains proper layout
- **Configuration**: JSON-based config file for customization

## Installation

```bash
# Make the wrapper executable
chmod +x claude_wrapper.py

# Optional: Create a symlink to use it globally
sudo ln -s /path/to/claude_wrapper.py /usr/local/bin/claude-wrapper
```

## Usage

### Basic Usage

```bash
# Run with default settings (1 second countdown)
./claude_wrapper.py

# With custom countdown delay
./claude_wrapper.py --delay 3

# With debug logging
./claude_wrapper.py --debug

# Disable auto-approve (just pass through to Claude)
./claude_wrapper.py --no-auto-approve

# Pass arguments to Claude Code
./claude_wrapper.py -- --tools default
```

### Configuration

Create a configuration file at `~/.claude_wrapper.conf`:

```json
{
  "auto_approve_delay": 1,
  "debug": false,
  "auto_approve_enabled": true,
  "show_status_bar": true,
  "toggle_key": "\u0001",
  "min_detection_score": 3,
  "max_approvals_per_minute": 500,
  "max_same_prompt_approvals": 5,
  "idle_detection_enabled": true,
  "idle_timeout_seconds": 2.5,
  "log_retention_days": 7
}
```

Generate default config:
```bash
./claude_wrapper.py --init-config
```

### Keyboard Controls

- **Any key**: Cancel countdown
- **Enter**: Approve immediately (during countdown)
- **Ctrl+A**: Toggle auto-approve on/off

## Testing

Run the test suite:

```bash
python3 test_wrapper.py
```

## How It Works

1. **PTY Wrapper**: Creates a pseudo-terminal to intercept Claude Code's output
2. **Prompt Detection**: Uses a scoring system to detect permission prompts
3. **Auto-Approval**: Shows countdown and automatically sends approval
4. **Safety Features**: Rate limiting and duplicate detection prevent loops

## Configuration Options

| Option | Default | Description |
|--------|---------|-------------|
| `auto_approve_delay` | 1 | Seconds to wait before auto-approving |
| `debug` | false | Enable debug logging to file |
| `auto_approve_enabled` | true | Enable/disable auto-approve |
| `show_status_bar` | true | Show status bar at bottom |
| `min_detection_score` | 3 | Minimum score to detect permission prompt |
| `max_approvals_per_minute` | 500 | Maximum total approvals per minute |
| `max_same_prompt_approvals` | 5 | Max times to approve same prompt in 60s |
| `idle_detection_enabled` | true | Enable idle detection fallback |
| `idle_timeout_seconds` | 2.5 | Seconds of no output before idle action |

## Requirements

- Python 3.7+
- Unix-like system (Linux, macOS)
- Claude Code CLI installed

## License

MIT

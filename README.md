# Claude Auto-Approve

[![Python Version](https://img.shields.io/badge/python-3.7%2B-blue.svg)](https://www.python.org/downloads/)
[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Code style: black](https://img.shields.io/badge/code%20style-black-000000.svg)](https://github.com/psf/black)

A production-ready wrapper for Claude Code that automatically approves permission prompts after a configurable countdown, while still presenting actual questions for user input.

## Features

- **Auto-approve permissions** with configurable countdown timer
- **Smart prompt detection** using multi-factor scoring system
- **Rate limiting** to prevent runaway approvals
- **Idle detection** fallback for stuck prompts
- **Status bar** showing countdown and approval count
- **Toggle auto-approve** on/off with Ctrl+A
- **Comprehensive logging** for debugging
- **Configurable** via JSON config file
- **Production-ready** with full test coverage and CI/CD
- **Modular architecture** with clean separation of concerns

## Installation

### From Source

```bash
# Clone the repository
git clone https://github.com/yourusername/claude-autoapprove.git
cd claude-autoapprove

# Install in development mode
pip install -e .

# Or install with development dependencies
pip install -e ".[dev]"
```

### From PyPI (once published)

```bash
pip install claude-autoapprove
```

## Quick Start

```bash
# Run with default settings
claude-wrapper

# Create a config file
claude-wrapper --init-config

# Run with custom delay
claude-wrapper --delay 3

# Run with debug logging
claude-wrapper --debug

# Disable auto-approve (just wrap Claude)
claude-wrapper --no-auto-approve

# Pass arguments to Claude Code
claude-wrapper -- --help
```

## Configuration

Create a configuration file at `~/.claude_wrapper.conf`:

```json
{
  "auto_approve_delay": 1,
  "debug": false,
  "log_dir": "~/.claude_wrapper_logs",
  "log_retention_days": 7,
  "claude_path": "claude",
  "auto_approve_enabled": true,
  "show_status_bar": true,
  "toggle_key": "\u0001",
  "min_detection_score": 3,
  "max_approvals_per_minute": 500,
  "max_same_prompt_approvals": 5,
  "idle_detection_enabled": true,
  "idle_timeout_seconds": 2.5,
  "patterns": {
    "permission_indicators": [],
    "text_input_indicators": [
      "Type.*yes",
      "Enter.*yes",
      "\\(y/n\\)"
    ]
  }
}
```

### Configuration Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `auto_approve_delay` | int | 1 | Seconds to wait before auto-approving |
| `debug` | bool | false | Enable debug logging |
| `auto_approve_enabled` | bool | true | Enable auto-approve on start |
| `show_status_bar` | bool | true | Show status bar at bottom |
| `min_detection_score` | int | 3 | Minimum score to detect permission prompt |
| `max_approvals_per_minute` | int | 500 | Maximum total approvals per minute |
| `max_same_prompt_approvals` | int | 5 | Maximum approvals of same prompt in 60s |
| `idle_detection_enabled` | bool | true | Enable idle detection fallback |
| `idle_timeout_seconds` | float | 2.5 | Seconds before idle approval |

## Keyboard Controls

- **Enter** during countdown: Approve immediately
- **Any key** during countdown: Cancel auto-approve
- **Ctrl+A**: Toggle auto-approve on/off
- **Esc**: Cancel (passed to Claude)

## Usage as a Library

```python
from claude_autoapprove import ClaudeWrapper, Config

# Create configuration
config = Config()
config.set("auto_approve_delay", 2)
config.set("debug", True)

# Create and run wrapper
wrapper = ClaudeWrapper(config)
exit_code = wrapper.run()
```

## Development

### Setup

```bash
# Install development dependencies
pip install -e ".[dev]"

# Install pre-commit hooks
pre-commit install
```

### Running Tests

```bash
# Run all tests with coverage
pytest --cov=claude_autoapprove --cov-report=term --cov-report=html

# Run specific test file
pytest tests/unit/test_config.py

# Run with verbose output
pytest -v
```

### Code Quality

```bash
# Format code
black src tests

# Sort imports
isort src tests

# Lint
flake8 src tests

# Type check
mypy src

# Run all pre-commit hooks
pre-commit run --all-files
```

## Architecture

The package is organized into focused modules:

- `constants.py` (~40 lines) - Configuration constants and patterns
- `exceptions.py` (~30 lines) - Custom exception classes
- `utils.py` (~100 lines) - Utility functions
- `config.py` (~200 lines) - Configuration management
- `terminal.py` (~200 lines) - Terminal and status bar management
- `detection.py` (~280 lines) - Prompt detection and rate limiting
- `approval.py` (~250 lines) - Approval countdown logic
- `wrapper.py` (~450 lines) - Main orchestration
- `cli.py` (~150 lines) - Command-line interface

Total: ~1,700 lines (vs. original 1,200-line monolithic file)

## How It Works

1. **Terminal Setup**: Creates a pseudo-terminal (PTY) and configures scrolling regions
2. **Prompt Detection**: Uses multi-factor scoring to identify permission prompts
3. **Countdown**: Shows countdown (configurable) in status bar
4. **User Input**: Allows cancellation or immediate approval
5. **Rate Limiting**: Prevents approval loops with duplicate detection
6. **Idle Detection**: Fallback mechanism for stuck prompts

## Safety Features

- **Smart Detection**: Requires multiple indicators (score ≥ 3) to avoid false positives
- **Rate Limiting**: Blocks repeated approvals of the same prompt
- **Global Rate Limit**: Maximum approvals per minute
- **Code Block Protection**: Never auto-approves within code blocks
- **Length Checks**: Reduces score for suspiciously long text
- **User Control**: Easy toggle and cancellation

## Requirements

- Python 3.7+
- Unix-like system (Linux, macOS)
- Claude Code CLI installed

## License

MIT License - see [LICENSE](LICENSE) file for details.

## Acknowledgments

Built with Claude Code and developed collaboratively with Claude AI.

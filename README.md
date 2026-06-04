# cry-aye

A lightweight PTY wrapper for Claude Code that automatically approves permission prompts.

## Install

```bash
git clone https://github.com/yourusername/cry-aye.git
cd cry-aye
make install        # builds and copies to /usr/local/bin
```

Or just build locally:

```bash
make build          # produces ./cry-aye
```

## Usage

```bash
# Drop-in replacement for the claude command
cry-aye

# Pass a prompt directly
cry-aye -- 'refactor this module'

# Add a 3-second countdown before each approval (default is 0)
cry-aye --delay 3

# Pass flags to Claude itself (use -- separator)
cry-aye -- --help
```

## Options

| Flag | Default | Description |
|------|---------|-------------|
| `--delay N` | `0` | Seconds to wait before approving (0–60). Use a non-zero value if you want to review and cancel. |
| `--help` | | Show help |

## Keyboard controls

| Key | Action |
|-----|--------|
| `Ctrl+A` | Toggle auto-approve on/off |
| `Enter` | Approve immediately during countdown |
| Any other key | Cancel countdown |
| `Ctrl+↑` | Increase delay by 1s |
| `Ctrl+↓` | Decrease delay by 1s |

## How it works

The wrapper runs `claude` inside a PTY and proxies all I/O transparently. Output bytes flow straight through to your terminal untouched. In parallel, the wrapper accumulates a rolling buffer and scores it against known Claude Code permission dialog patterns ("1. Yes", "Enter to confirm", etc.). When the score crosses the threshold a countdown starts; when it expires (or Enter is pressed), `\r` or `yes\r` is written to the PTY input.

After each approval `ForceRedraw()` sends a brief PTY size toggle (`width-1` → `width`) to make Claude fully repaint its UI and surface any immediately-following dialog.

A 200ms ticker runs two background jobs:
- **Missed-prompt watchdog** — detects prompts that arrived while a countdown was active (and therefore blocked from detection).
- **Idle rescue** — if Claude has been silent for 2+ seconds, triggers a `ForceRedraw()` so any pending dialog re-flows through the output path.

## Detection

Scores the last 50 stripped lines of the buffer:

| Indicator | Score |
|-----------|-------|
| `1. Yes` + `2./3. No` buttons | +5 |
| `Enter to approve` / `Enter to confirm` | +3 |
| `(y/n)` at end of line | +3 |
| `Permission rule` header | +3 |
| `Esc to cancel` | +2 |
| `Tab to amend` | +2 |

**Threshold:** ≥ 3 to trigger.

## Dependencies

- [`github.com/creack/pty`](https://github.com/creack/pty)
- [`golang.org/x/term`](https://golang.org/x/term)

## Requirements

- Go 1.21+
- macOS or Linux
- `claude` CLI in PATH

## Debug logging

```bash
DEBUG_AUTOAPPROVE=1 cry-aye
tail -f ~/.cry-aye-debug.log
```

## License

MIT

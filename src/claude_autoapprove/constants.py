"""
Constants and configuration defaults for Claude Auto-Approve.
"""

import re
from pathlib import Path

# Version
__version__ = "1.1.0"

# Default configuration paths
DEFAULT_CONFIG_PATH = Path.home() / ".claude_wrapper.conf"
DEFAULT_LOG_DIR = Path.home() / ".claude_wrapper_logs"

# Default behavior settings
DEFAULT_AUTO_APPROVE_DELAY = 1

# Buffer and I/O settings
MAX_BUFFER_SIZE = 10000
READ_SIZE = 1024

# ANSI escape code patterns
# Pattern for all ANSI escape sequences
ANSI_ESCAPE_PATTERN = re.compile(r"\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~]|\][^\x07]*\x07)")

# Pattern for cursor movement codes specifically (should be replaced with space)
# Comprehensive pattern covering: A-G (cursor moves), H/f (positioning), J/K (erase), s/u (save/restore)
ANSI_CURSOR_PATTERN = re.compile(r"\x1b\[[\d;]*[ABCDEFGHJKfsu]")

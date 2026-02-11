"""
Claude Auto-Approve - Production-ready wrapper for Claude Code.

A wrapper for Claude Code that automatically approves permission prompts
after a configurable countdown, while still presenting actual questions
for user input.
"""

from .config import Config
from .constants import __version__
from .exceptions import (
    ClaudeNotFoundError,
    ClaudeWrapperError,
    ConfigurationError,
    RateLimitError,
    TerminalSetupError,
)
from .wrapper import ClaudeWrapper

__all__ = [
    # Version
    "__version__",
    # Main classes
    "ClaudeWrapper",
    "Config",
    # Exceptions
    "ClaudeWrapperError",
    "ClaudeNotFoundError",
    "TerminalSetupError",
    "ConfigurationError",
    "RateLimitError",
]

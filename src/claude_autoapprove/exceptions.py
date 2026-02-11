"""
Custom exceptions for Claude Auto-Approve.
"""


class ClaudeWrapperError(Exception):
    """Base exception for Claude Wrapper errors."""

    pass


class ClaudeNotFoundError(ClaudeWrapperError):
    """Raised when Claude Code executable is not found."""

    pass


class TerminalSetupError(ClaudeWrapperError):
    """Raised when terminal setup fails."""

    pass


class ConfigurationError(ClaudeWrapperError):
    """Raised when configuration is invalid."""

    pass


class RateLimitError(ClaudeWrapperError):
    """Raised when rate limit is exceeded."""

    pass

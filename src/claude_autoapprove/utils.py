"""
Utility functions for Claude Auto-Approve.
"""

import logging
import shutil
import time
from pathlib import Path
from typing import Optional


def find_executable(name: str) -> Optional[str]:
    """
    Find an executable in the system PATH.

    Args:
        name: Name of the executable to find

    Returns:
        Full path to executable if found, None otherwise

    Example:
        >>> find_executable("claude")
        '/usr/local/bin/claude'
    """
    return shutil.which(name)


def cleanup_old_files(directory: Path, pattern: str, max_age_days: int, debug: bool = False) -> int:
    """
    Remove files older than specified age from a directory.

    Args:
        directory: Directory to clean up
        pattern: Glob pattern for files to consider (e.g., "wrapper_*.log")
        max_age_days: Maximum age in days before deletion
        debug: Whether to log debug messages

    Returns:
        Number of files deleted

    Example:
        >>> cleanup_old_files(Path("/tmp/logs"), "*.log", 7)
        3
    """
    if not directory.exists():
        return 0

    deleted_count = 0
    current_time = time.time()
    max_age_seconds = max_age_days * 86400  # days to seconds

    for file_path in directory.glob(pattern):
        try:
            file_age = current_time - file_path.stat().st_mtime
            if file_age > max_age_seconds:
                file_path.unlink()
                deleted_count += 1
                if debug:
                    logging.debug(f"Removed old file: {file_path}")
        except Exception as e:
            # Ignore errors for individual files
            if debug:
                logging.debug(f"Failed to delete {file_path}: {e}")

    return deleted_count


def format_time(seconds: float) -> str:
    """
    Format time duration in a human-readable way.

    Args:
        seconds: Time duration in seconds

    Returns:
        Formatted string (e.g., "2.5s", "1m 30s")

    Example:
        >>> format_time(2.5)
        '2.5s'
        >>> format_time(90)
        '1m 30s'
    """
    if seconds < 60:
        return f"{seconds:.1f}s"

    minutes = int(seconds // 60)
    remaining_seconds = int(seconds % 60)
    return f"{minutes}m {remaining_seconds}s"

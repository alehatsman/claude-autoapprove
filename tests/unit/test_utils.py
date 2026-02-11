"""
Unit tests for utility functions.
"""

import tempfile
import time
from pathlib import Path

import pytest

from claude_autoapprove.utils import cleanup_old_files, find_executable, format_time


class TestUtils:
    """Test utility functions."""

    def test_find_executable_existing(self):
        """Test finding an executable that exists."""
        # Most systems have 'ls' or 'echo'
        result = find_executable("ls") or find_executable("echo")
        assert result is not None
        assert isinstance(result, str)

    def test_find_executable_nonexistent(self):
        """Test finding an executable that doesn't exist."""
        result = find_executable("this_executable_definitely_does_not_exist_12345")
        assert result is None

    def test_cleanup_old_files(self):
        """Test cleanup of old files."""
        with tempfile.TemporaryDirectory() as tmpdir:
            tmp_path = Path(tmpdir)

            # Create some test files
            old_file = tmp_path / "test_old.log"
            new_file = tmp_path / "test_new.log"

            old_file.write_text("old")
            new_file.write_text("new")

            # Set old file's modification time to 10 days ago
            old_time = time.time() - (10 * 86400)
            import os

            os.utime(old_file, (old_time, old_time))

            # Cleanup files older than 7 days
            deleted_count = cleanup_old_files(tmp_path, "test_*.log", 7, debug=False)

            assert deleted_count == 1
            assert not old_file.exists()
            assert new_file.exists()

    def test_cleanup_old_files_nonexistent_dir(self):
        """Test cleanup with non-existent directory."""
        nonexistent = Path("/tmp/nonexistent_dir_12345")
        deleted_count = cleanup_old_files(nonexistent, "*.log", 7)
        assert deleted_count == 0

    @pytest.mark.parametrize(
        "seconds,expected",
        [
            (2.5, "2.5s"),
            (30.0, "30.0s"),
            (59.9, "59.9s"),
            (60.0, "1m 0s"),
            (90.0, "1m 30s"),
            (125.7, "2m 5s"),
        ],
    )
    def test_format_time(self, seconds, expected):
        """Test time formatting."""
        result = format_time(seconds)
        assert result == expected

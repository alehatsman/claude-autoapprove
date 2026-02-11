"""
Terminal management for Claude Auto-Approve.

Handles terminal setup, size management, and status bar display.
"""

import fcntl
import logging
import struct
import sys
import termios
from typing import Optional

from .exceptions import TerminalSetupError


class TerminalManager:
    """
    Manages terminal state and sizing.

    Handles:
    - Terminal size detection and updates
    - PTY size synchronization
    - Scrolling region setup
    - Terminal state preservation and restoration

    Attributes:
        master_fd: File descriptor for PTY master
        original_tty: Original terminal attributes (for restoration)
        term_height: Current terminal height in rows
        term_width: Current terminal width in columns
        content_rows: Number of rows available for content (excluding status bar)
        status_bar_row: Row number where status bar starts
        show_status_bar: Whether status bar is enabled

    Example:
        >>> term = TerminalManager(master_fd, show_status_bar=True)
        >>> term.setup()
        >>> term.update_size()
    """

    def __init__(self, master_fd: int, show_status_bar: bool = True, debug: bool = False):
        """
        Initialize terminal manager.

        Args:
            master_fd: File descriptor for PTY master
            show_status_bar: Whether to reserve space for status bar
            debug: Enable debug logging
        """
        self.master_fd = master_fd
        self.show_status_bar = show_status_bar
        self.debug = debug
        self.original_tty: Optional[list] = None

        # Default dimensions
        self.term_height = 24
        self.term_width = 80
        self.content_rows = 23
        self.status_bar_row = 24

    def save_tty_state(self) -> None:
        """Save current terminal state for later restoration."""
        if sys.stdin.isatty():
            self.original_tty = termios.tcgetattr(sys.stdin)

    def restore_tty_state(self) -> None:
        """Restore original terminal state."""
        if self.original_tty and sys.stdin.isatty():
            try:
                termios.tcsetattr(sys.stdin, termios.TCSADRAIN, self.original_tty)
                logging.debug("Terminal settings restored")
            except Exception as e:
                logging.error(f"Failed to restore terminal: {e}")

    def update_size(self) -> None:
        """
        Update terminal size and synchronize with PTY.

        Reads current terminal dimensions and updates PTY size accordingly.
        Recalculates content area and status bar position.

        Raises:
            TerminalSetupError: If unable to get or set terminal size
        """
        if not sys.stdout.isatty():
            return

        try:
            # Get current terminal size
            size = struct.unpack(
                "HHHH",
                fcntl.ioctl(
                    sys.stdout.fileno(), termios.TIOCGWINSZ, struct.pack("HHHH", 0, 0, 0, 0)
                ),
            )
            rows, cols = size[0], size[1]

            # Update terminal dimensions
            self.term_height = rows
            self.term_width = cols

            # Calculate split: reserve rows for status bar
            status_bar_height = 2 if self.show_status_bar else 0
            if rows < 10 and self.show_status_bar:
                status_bar_height = 1  # Minimal status for tiny terminals

            self.content_rows = max(1, rows - status_bar_height)
            self.status_bar_row = self.content_rows + 1

            # Set PTY size (rows, cols, xpixel, ypixel)
            fcntl.ioctl(self.master_fd, termios.TIOCSWINSZ, struct.pack("HHHH", *size))

            # Set up scrolling region: top to content_rows
            if self.show_status_bar:
                sys.stdout.write(f"\033[1;{self.content_rows}r")
                sys.stdout.flush()

            if self.debug:
                logging.debug(
                    f"Terminal size set - Rows: {rows}, Cols: {cols}, "
                    f"Content rows: {self.content_rows}, Status bar row: {self.status_bar_row}"
                )
        except Exception as e:
            if self.debug:
                logging.debug(f"Error setting terminal size: {e}")
            raise TerminalSetupError(f"Failed to update terminal size: {e}")

    def reset_scrolling_region(self) -> None:
        """Reset scrolling region to full screen."""
        try:
            sys.stdout.write("\033[r")
            sys.stdout.flush()
        except Exception as e:
            logging.error(f"Failed to reset scrolling region: {e}")

    def clear_from_row(self, row: int) -> None:
        """
        Clear screen from specified row to end.

        Args:
            row: Row number to start clearing from
        """
        try:
            sys.stderr.write(f"\033[{row};1H")
            sys.stderr.write("\033[J")  # Clear from cursor to end of screen
            sys.stderr.flush()
        except Exception as e:
            logging.debug(f"Failed to clear from row {row}: {e}")


class StatusBar:
    """
    Manages status bar display at bottom of terminal.

    Draws messages in a fixed area at the bottom of the terminal,
    outside the main scrolling region.

    Attributes:
        terminal: TerminalManager instance
        pid: Process ID to display in status messages

    Example:
        >>> status = StatusBar(terminal, pid=12345)
        >>> status.draw("Ready", color_code="32")
        >>> status.clear()
    """

    def __init__(self, terminal: TerminalManager, pid: int):
        """
        Initialize status bar.

        Args:
            terminal: TerminalManager instance
            pid: Process ID for display
        """
        self.terminal = terminal
        self.pid = pid

    def draw(self, message: str, color_code: str = "33") -> None:
        """
        Draw a message in the status bar area.

        Args:
            message: Message to display
            color_code: ANSI color code (default: "33" for yellow)

        Example:
            >>> status.draw("Auto-approving in 3s...", color_code="33")
        """
        if not self.terminal.show_status_bar:
            return

        try:
            # Save cursor position
            sys.stderr.write("\0337")

            # Move to status bar area
            sys.stderr.write(f"\033[{self.terminal.status_bar_row};1H")

            # Clear the status bar area
            for row in range(self.terminal.status_bar_row, self.terminal.term_height + 1):
                sys.stderr.write(f"\033[{row};1H\033[K")

            # Draw top border of status bar
            sys.stderr.write(f"\033[{self.terminal.status_bar_row};1H")
            sys.stderr.write(f"\033[2m{'─' * self.terminal.term_width}\033[0m")

            # Draw message on next line
            sys.stderr.write(f"\033[{self.terminal.status_bar_row + 1};1H")
            sys.stderr.write(f"\033[{color_code}m{message}\033[0m")

            # Restore cursor position
            sys.stderr.write("\0338")
            sys.stderr.flush()
        except Exception as e:
            logging.debug(f"Failed to draw status bar: {e}")
            # Try to restore cursor at least
            try:
                sys.stderr.write("\0338")
                sys.stderr.flush()
            except:
                pass

    def clear(self, auto_approve_enabled: bool, approval_count: int = 0) -> None:
        """
        Show idle/ready state in status bar.

        Args:
            auto_approve_enabled: Whether auto-approve is currently enabled
            approval_count: Number of approvals executed so far

        Example:
            >>> status.clear(auto_approve_enabled=True, approval_count=5)
        """
        if auto_approve_enabled:
            if approval_count > 0:
                msg = f"[PID {self.pid}] Ready (auto-approve ON, {approval_count} executed) [Ctrl+A to toggle]"
                self.draw(msg, "2")
            else:
                msg = f"[PID {self.pid}] Ready (auto-approve ON) [Ctrl+A to toggle]"
                self.draw(msg, "2")
        else:
            msg = f"[PID {self.pid}] Ready (auto-approve OFF) [Ctrl+A to toggle]"
            self.draw(msg, "90")

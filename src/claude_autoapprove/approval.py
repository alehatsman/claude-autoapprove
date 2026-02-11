"""
Approval management and countdown logic for Claude Auto-Approve.

Handles:
- Countdown timer before auto-approval
- Immediate approval on Enter press
- Cancellation on user input
- Sending approval commands to Claude
"""

import logging
import os
import threading
import time
from typing import Optional

from .detection import PromptDetector, RateLimiter
from .terminal import StatusBar


class ApprovalManager:
    """
    Manages approval countdown and execution.

    Coordinates between:
    - Countdown timer thread
    - User input (immediate approval or cancellation)
    - Rate limiting
    - Approval execution

    Attributes:
        config: Configuration dictionary
        detector: PromptDetector instance
        rate_limiter: RateLimiter instance
        status_bar: StatusBar instance
        master_fd: File descriptor for PTY master
        debug: Whether debug logging is enabled
        approval_count: Total number of approvals executed
        last_approval_time: Timestamp of last approval
        countdown_running: Whether countdown is currently active
        countdown_cancelled: Event to signal countdown cancellation
        countdown_approve_now: Event to signal immediate approval
        countdown_thread: Active countdown thread (if any)
        countdown_lock: Lock for thread-safe countdown state

    Example:
        >>> manager = ApprovalManager(config, detector, rate_limiter, status_bar, master_fd)
        >>> manager.start_countdown(buffer_text, delay_seconds=3)
        >>> manager.cancel_countdown()
    """

    def __init__(
        self,
        config: dict,
        detector: PromptDetector,
        rate_limiter: RateLimiter,
        status_bar: StatusBar,
        master_fd: int,
        debug: bool = False,
    ):
        """
        Initialize approval manager.

        Args:
            config: Configuration dictionary
            detector: PromptDetector instance
            rate_limiter: RateLimiter instance
            status_bar: StatusBar instance for display
            master_fd: File descriptor for PTY master
            debug: Enable debug logging
        """
        self.config = config
        self.detector = detector
        self.rate_limiter = rate_limiter
        self.status_bar = status_bar
        self.master_fd = master_fd
        self.debug = debug

        # State
        self.approval_count = 0
        self.last_approval_time = 0.0
        self._countdown_running = False
        self._countdown_cancelled = threading.Event()
        self._countdown_approve_now = threading.Event()
        self._countdown_thread: Optional[threading.Thread] = None
        self._countdown_lock = threading.Lock()
        self._last_buffer = ""

    def countdown_and_approve(self, seconds: int) -> None:
        """
        Show countdown and auto-approve, cancellable by user input.

        Args:
            seconds: Number of seconds to count down

        Example:
            >>> manager.countdown_and_approve(3)
        """
        # Countdown loop
        for i in range(seconds, 0, -1):
            # Check if user wants to approve immediately
            if self._countdown_approve_now.is_set():
                break

            # Check if cancelled
            if self._countdown_cancelled.is_set():
                self.status_bar.draw("✗ Auto-approve cancelled", "90")
                time.sleep(0.3)
                self.status_bar.clear(auto_approve_enabled=True, approval_count=self.approval_count)
                return

            # Draw countdown message
            self.status_bar.draw(
                f"⏱  Auto-approving in {i}s... (Enter=approve now, any key=cancel, Ctrl+A=toggle off)",
                "33",
            )
            time.sleep(1)

        # Final check before approving
        if self._countdown_cancelled.is_set():
            self.status_bar.draw("✗ Auto-approve cancelled", "90")
            time.sleep(0.3)
            self.status_bar.clear(auto_approve_enabled=True, approval_count=self.approval_count)
            return

        # Execute approval
        self._execute_approval()

    def _execute_approval(self) -> None:
        """Execute the approval by sending appropriate input to PTY."""
        # Increment counter and record time
        self.approval_count += 1
        self.last_approval_time = time.time()

        # Show approval message
        if not self._countdown_approve_now.is_set():
            self.status_bar.draw(f"✓ Auto-approved (#{self.approval_count})", "32")
        else:
            self.status_bar.draw(f"✓ Approved immediately (#{self.approval_count})", "32")
        time.sleep(0.3)

        # Determine what to send based on prompt type
        prompt_type = self.detector.get_prompt_type(self._last_buffer)

        if self.debug:
            logging.debug(f"Sending approval - Prompt type: {prompt_type}, fd: {self.master_fd}")

        time.sleep(0.1)  # Small delay to ensure prompt is ready

        if prompt_type == "numbered_menu":
            # For numbered menu, just press Enter
            bytes_written = os.write(self.master_fd, b"\r")
            if self.debug:
                logging.debug(f"Wrote {bytes_written} bytes (Enter for numbered menu)")
        else:
            # Send 'yes' for text input prompts
            bytes_written = os.write(self.master_fd, b"yes")
            if self.debug:
                logging.debug(f"Wrote {bytes_written} bytes (text 'yes')")

            time.sleep(0.1)

            # Send Enter
            bytes_written = os.write(self.master_fd, b"\r")
            if self.debug:
                logging.debug(f"Wrote {bytes_written} bytes (Enter after 'yes')")

        # Return to ready state
        self.status_bar.clear(auto_approve_enabled=True, approval_count=self.approval_count)

    def start_countdown(self, buffer: str, delay: int) -> bool:
        """
        Start approval countdown for a detected prompt.

        Args:
            buffer: Buffer text containing the prompt
            delay: Delay in seconds before approval

        Returns:
            True if countdown started, False if blocked or already running

        Example:
            >>> if manager.start_countdown(text, 3):
            ...     print("Countdown started")
        """
        # Check rate limits
        allowed, reason = self.rate_limiter.check_approval_allowed(buffer, self.detector.strip_ansi)
        if not allowed:
            # Show warning to user
            self.status_bar.draw(f"⚠  {reason}", "31")
            time.sleep(2)
            return False

        # Use lock to prevent race condition
        with self._countdown_lock:
            # If countdown already running, cancel it and start new one
            if self._countdown_running:
                if self.debug:
                    logging.debug("Cancelling old countdown for new prompt")
                self._countdown_cancelled.set()
                self._countdown_running = False

            if not self._countdown_running:
                self._countdown_running = True
                self._countdown_cancelled.clear()
                self._countdown_approve_now.clear()
                self._last_buffer = buffer

                # Record this approval attempt
                self.rate_limiter.record_approval(buffer, self.detector.strip_ansi)

                def countdown_wrapper():
                    try:
                        self.countdown_and_approve(delay)
                    finally:
                        with self._countdown_lock:
                            self._countdown_running = False

                # Start countdown in separate thread
                self._countdown_thread = threading.Thread(target=countdown_wrapper)
                self._countdown_thread.daemon = True
                self._countdown_thread.start()
                return True

        return False

    def cancel_countdown(self) -> None:
        """
        Cancel any running countdown.

        Example:
            >>> manager.cancel_countdown()
        """
        if self._countdown_running:
            self._countdown_cancelled.set()
            if self.debug:
                logging.debug("Countdown cancelled by user input")

    def approve_now(self) -> None:
        """
        Signal immediate approval (skip countdown).

        Example:
            >>> manager.approve_now()
        """
        if self._countdown_running:
            self._countdown_approve_now.set()
            if self.debug:
                logging.debug("Immediate approval requested")

    def is_running(self) -> bool:
        """
        Check if countdown is currently running.

        Returns:
            True if countdown is active

        Example:
            >>> if manager.is_running():
            ...     print("Countdown in progress")
        """
        return self._countdown_running

    def wait_for_completion(self, timeout: float = 2.0) -> bool:
        """
        Wait for countdown thread to complete.

        Args:
            timeout: Maximum time to wait in seconds

        Returns:
            True if thread completed, False if timeout

        Example:
            >>> manager.wait_for_completion(timeout=5.0)
        """
        if self._countdown_thread and self._countdown_thread.is_alive():
            self._countdown_thread.join(timeout=timeout)
            return not self._countdown_thread.is_alive()
        return True

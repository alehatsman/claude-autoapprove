"""
Main wrapper orchestration for Claude Auto-Approve.

Coordinates all components and manages the main I/O loop.
"""

import logging
import os
import pty
import select
import signal
import sys
import threading
import time
import tty
from pathlib import Path
from subprocess import Popen
from typing import Optional

from .approval import ApprovalManager
from .config import Config
from .constants import MAX_BUFFER_SIZE, READ_SIZE
from .detection import PromptDetector, RateLimiter
from .exceptions import ClaudeNotFoundError, TerminalSetupError
from .terminal import StatusBar, TerminalManager
from .utils import cleanup_old_files, find_executable


class ClaudeWrapper:
    """
    Main wrapper class for Claude Code with auto-approval functionality.

    Orchestrates all components:
    - Terminal management
    - Prompt detection
    - Approval countdown
    - Rate limiting
    - I/O handling

    Attributes:
        config: Configuration instance
        auto_approve_enabled: Whether auto-approve is currently enabled
        terminal: TerminalManager instance
        status_bar: StatusBar instance
        detector: PromptDetector instance
        rate_limiter: RateLimiter instance
        approval_manager: ApprovalManager instance
        master_fd: PTY master file descriptor
        process: Claude Code subprocess
        shutdown: Event to signal shutdown

    Example:
        >>> config = Config()
        >>> wrapper = ClaudeWrapper(config)
        >>> exit_code = wrapper.run(['--help'])
    """

    def __init__(self, config: Config):
        """
        Initialize Claude wrapper.

        Args:
            config: Configuration instance
        """
        self.config = config
        self.auto_approve_enabled = config.get("auto_approve_enabled", True)
        self.auto_approve_delay = config.get("auto_approve_delay", 1)
        self.debug = config.get("debug", False)
        self.claude_path = config.get("claude_path", "claude")

        # Process ID
        self.pid = os.getpid()

        # Setup logging
        log_dir = Path(config.get("log_dir", str(Path.home() / ".claude_wrapper_logs")))
        self.log_file = log_dir / f"wrapper_{self.pid}.log"
        self._setup_logging()

        # State
        self.master_fd: Optional[int] = None
        self.process: Optional[Popen] = None
        self._shutdown = threading.Event()
        self._last_output_time = time.time()
        self._last_idle_action_time = 0.0

        # Components (initialized in setup)
        self.terminal: Optional[TerminalManager] = None
        self.status_bar: Optional[StatusBar] = None
        self.detector: Optional[PromptDetector] = None
        self.rate_limiter: Optional[RateLimiter] = None
        self.approval_manager: Optional[ApprovalManager] = None

    def _setup_logging(self) -> None:
        """Configure logging."""
        if self.debug:
            # Ensure log directory exists
            self.log_file.parent.mkdir(parents=True, exist_ok=True)

            # Clean up old log files
            cleanup_old_files(
                self.log_file.parent,
                "wrapper_*.log",
                self.config.get("log_retention_days", 7),
                debug=False,
            )

            logging.basicConfig(
                level=logging.DEBUG,
                format="%(asctime)s - PID:%(process)d - %(levelname)s - %(message)s",
                handlers=[logging.FileHandler(self.log_file), logging.StreamHandler(sys.stderr)],
            )
            logging.info(f"Debug mode enabled - Log file: {self.log_file}")
            logging.info(f"Process ID: {self.pid}")
        else:
            logging.basicConfig(level=logging.ERROR)

    def _check_claude_exists(self) -> bool:
        """Verify that Claude Code executable exists."""
        return find_executable(self.claude_path) is not None

    def _validate_terminal(self) -> None:
        """
        Validate terminal requirements.

        Raises:
            TerminalSetupError: If terminal is not suitable
        """
        if not sys.stdin.isatty():
            raise TerminalSetupError("stdin is not a TTY")
        if not sys.stdout.isatty():
            raise TerminalSetupError("stdout is not a TTY")

    def _initialize_components(self) -> None:
        """Initialize all components with proper dependencies."""
        # Terminal management
        self.terminal = TerminalManager(
            self.master_fd,
            show_status_bar=self.config.get("show_status_bar", True),
            debug=self.debug,
        )
        self.terminal.save_tty_state()

        # Status bar
        self.status_bar = StatusBar(self.terminal, self.pid)

        # Detection
        self.detector = PromptDetector(self.config.config, debug=self.debug)
        self.rate_limiter = RateLimiter(self.config.config, debug=self.debug)

        # Approval management
        self.approval_manager = ApprovalManager(
            self.config.config,
            self.detector,
            self.rate_limiter,
            self.status_bar,
            self.master_fd,
            debug=self.debug,
        )

    def handle_shutdown_signal(self, signum: int, frame) -> None:
        """
        Handle shutdown signals gracefully.

        Args:
            signum: Signal number
            frame: Current stack frame
        """
        logging.info(f"Received signal {signum}, shutting down...")
        self._shutdown.set()
        self.cleanup()
        sys.exit(128 + signum)

    def handle_sigwinch(self, signum: int, frame) -> None:
        """
        Handle terminal resize events.

        Args:
            signum: Signal number (SIGWINCH)
            frame: Current stack frame
        """
        if self.terminal:
            self.terminal.update_size()

    def cleanup(self) -> None:
        """Cleanup resources and restore terminal state."""
        logging.debug("Cleaning up...")

        # Cancel any running countdown
        if self.approval_manager and self.approval_manager.is_running():
            self.approval_manager.cancel_countdown()
            self.approval_manager.wait_for_completion(timeout=2.0)

        # Clear status bar area
        if self.terminal:
            self.terminal.clear_from_row(self.terminal.status_bar_row)
            self.terminal.reset_scrolling_region()
            self.terminal.restore_tty_state()

        # Close master fd
        if self.master_fd:
            try:
                os.close(self.master_fd)
                logging.debug("Master FD closed")
            except Exception as e:
                logging.error(f"Failed to close master fd: {e}")

        # Terminate process
        if self.process and self.process.poll() is None:
            try:
                self.process.terminate()
                self.process.wait(timeout=5)
                logging.debug("Process terminated")
            except Exception as e:
                logging.error(f"Failed to terminate process: {e}")
                try:
                    self.process.kill()
                except:
                    pass

    def _handle_user_input(self, char: bytes, output_buffer: str) -> tuple[bool, str]:
        """
        Handle input from user.

        Args:
            char: Input character(s)
            output_buffer: Current output buffer

        Returns:
            Tuple of (continue: bool, updated_buffer: str)
        """
        # Check if Enter pressed during countdown - approve immediately
        if self.approval_manager.is_running() and char in (b"\r", b"\n"):
            self.approval_manager.approve_now()
            return True, output_buffer  # Don't forward Enter to Claude

        # Check for toggle key (Ctrl+A)
        toggle_key = self.config.get("toggle_key", "\x01").encode()
        if char == toggle_key:
            # Toggle auto-approve
            self.auto_approve_enabled = not self.auto_approve_enabled

            # Cancel any running countdown
            if self.approval_manager.is_running():
                self.approval_manager.cancel_countdown()

            # Show toggle message
            if self.auto_approve_enabled:
                self.status_bar.draw("✓ Auto-approve ENABLED", "32")
            else:
                self.status_bar.draw("✗ Auto-approve DISABLED", "31")

            time.sleep(0.8)
            self.status_bar.clear(self.auto_approve_enabled, self.approval_manager.approval_count)

            if self.debug:
                logging.debug(
                    f"Auto-approve toggled: {'ON' if self.auto_approve_enabled else 'OFF'}"
                )

            # If re-enabled, check if there's already a prompt in the buffer
            if self.auto_approve_enabled and output_buffer:
                if self.detector.is_permission_prompt(output_buffer):
                    if self.debug:
                        logging.debug("Detected existing prompt after re-enabling auto-approve")
                    was_detected = self.approval_manager.start_countdown(
                        output_buffer, self.auto_approve_delay
                    )
                    if was_detected:
                        output_buffer = ""

            return True, output_buffer  # Don't forward toggle key to Claude

        # Cancel countdown if user presses any other key
        if self.approval_manager.is_running():
            self.approval_manager.cancel_countdown()

        # Forward input to Claude
        os.write(self.master_fd, char)
        return True, output_buffer

    def _handle_claude_output(self, data: bytes, output_buffer: str) -> tuple[str, bool]:
        """
        Handle output from Claude.

        Args:
            data: Raw output data
            output_buffer: Current output buffer

        Returns:
            Tuple of (updated_buffer, was_prompt_detected)
        """
        # Update last output time
        self._last_output_time = time.time()

        # Write to stdout
        os.write(sys.stdout.fileno(), data)

        # Always add to buffer for prompt detection (even when disabled)
        # This ensures we have context when re-enabling auto-approve
        try:
            decoded = data.decode("utf-8", errors="replace")
            output_buffer += decoded
        except Exception as e:
            logging.warning(f"Failed to decode output data: {e}")
            return output_buffer, False

        # Keep buffer size manageable
        if len(output_buffer) > MAX_BUFFER_SIZE:
            trim_point = int(MAX_BUFFER_SIZE * 0.2)
            output_buffer = output_buffer[trim_point:]

        # Only check for prompts if auto-approve is enabled
        was_detected = False
        if self.auto_approve_enabled:
            if self.detector.is_permission_prompt(output_buffer):
                was_detected = self.approval_manager.start_countdown(
                    output_buffer, self.auto_approve_delay
                )

        # Clear buffer if prompt detected
        if was_detected:
            output_buffer = ""
            if self.debug:
                logging.debug("Buffer cleared after prompt detection")

        return output_buffer, was_detected

    def _check_idle_state(self, output_buffer: str) -> str:
        """
        Check for idle state and trigger approval if needed.

        Args:
            output_buffer: Current output buffer

        Returns:
            Updated output buffer (cleared if action taken)
        """
        if not self.config.get("idle_detection_enabled", True):
            return output_buffer

        if not self.auto_approve_enabled or self.approval_manager.is_running():
            return output_buffer

        current_time = time.time()
        idle_timeout = self.config.get("idle_timeout_seconds", 2.5)
        time_since_output = current_time - self._last_output_time
        time_since_idle_action = current_time - self._last_idle_action_time

        # Only act if idle long enough and buffer has content
        if time_since_output >= idle_timeout and time_since_idle_action >= idle_timeout:
            if len(output_buffer) > 50:
                if self.debug:
                    logging.debug(
                        f"Idle detection triggered - "
                        f"Time since output: {time_since_output:.2f}s"
                    )

                self.status_bar.draw("⏱  Idle detected, auto-approving...", "33")
                time.sleep(0.3)

                # Determine prompt type and send approval
                prompt_type = self.detector.get_prompt_type(output_buffer)
                time.sleep(0.1)

                if prompt_type == "numbered_menu":
                    os.write(self.master_fd, b"\r")
                else:
                    os.write(self.master_fd, b"yes")
                    time.sleep(0.1)
                    os.write(self.master_fd, b"\r")

                # Update counters
                self._last_idle_action_time = current_time
                self.approval_manager.approval_count += 1
                self.approval_manager.last_approval_time = current_time
                self.rate_limiter.approval_timestamps.append(current_time)

                self.status_bar.draw(
                    f"✓ Auto-approved via idle detection (#{self.approval_manager.approval_count})",
                    "32",
                )
                time.sleep(0.3)
                self.status_bar.clear(
                    self.auto_approve_enabled, self.approval_manager.approval_count
                )

                return ""  # Clear buffer

        return output_buffer

    def run(self, args: Optional[list] = None) -> int:
        """
        Run Claude Code with the wrapper.

        Args:
            args: Optional arguments to pass to Claude Code

        Returns:
            Exit code from Claude process

        Raises:
            ClaudeNotFoundError: If Claude executable not found
            TerminalSetupError: If terminal setup fails

        Example:
            >>> wrapper = ClaudeWrapper(config)
            >>> exit_code = wrapper.run(['--help'])
        """
        # Validate environment
        if not self._check_claude_exists():
            raise ClaudeNotFoundError(
                f"Claude Code executable '{self.claude_path}' not found in PATH. "
                "Please install Claude Code or specify the correct path in config."
            )

        self._validate_terminal()

        # Build command
        cmd = [self.claude_path]
        if args:
            cmd.extend(args)

        logging.info(f"Starting Claude Code: {' '.join(cmd)}")

        # Setup signal handlers
        signal.signal(signal.SIGINT, self.handle_shutdown_signal)
        signal.signal(signal.SIGTERM, self.handle_shutdown_signal)
        signal.signal(signal.SIGHUP, self.handle_shutdown_signal)

        try:
            # Create pseudo-terminal
            self.master_fd, slave_fd = pty.openpty()

            # Start Claude process
            self.process = Popen(
                cmd, stdin=slave_fd, stdout=slave_fd, stderr=slave_fd, close_fds=True
            )
            os.close(slave_fd)

            # Initialize components
            self._initialize_components()

            # Clear screen
            sys.stdout.write("\033[2J\033[H")
            sys.stdout.flush()

            # Set initial terminal size
            self.terminal.update_size()

            # Initialize status bar
            if self.auto_approve_enabled and self.config.get("show_status_bar", True):
                self.status_bar.clear(self.auto_approve_enabled, 0)

            # Set up SIGWINCH handler
            signal.signal(signal.SIGWINCH, self.handle_sigwinch)

            # Set terminal to raw mode
            if sys.stdin.isatty():
                tty.setraw(sys.stdin.fileno())

            output_buffer = ""

            # Main I/O loop
            while not self._shutdown.is_set():
                # Check if process is still alive
                if self.process.poll() is not None:
                    logging.debug("Process exited")
                    break

                # Monitor stdin and master_fd
                try:
                    r, w, e = select.select([sys.stdin, self.master_fd], [], [], 0.1)
                except select.error as err:
                    if err.args[0] == 4:  # EINTR
                        continue
                    logging.error(f"Select error: {err}")
                    break

                # Handle user input
                if sys.stdin in r:
                    try:
                        char = os.read(sys.stdin.fileno(), READ_SIZE)
                        if not char:
                            logging.debug("EOF on stdin")
                            break

                        should_continue, output_buffer = self._handle_user_input(char, output_buffer)
                        if not should_continue:
                            break
                    except OSError as e:
                        logging.error(f"Error reading from stdin: {e}")
                        break

                # Handle Claude output
                if self.master_fd in r:
                    try:
                        data = os.read(self.master_fd, READ_SIZE)
                        if not data:
                            logging.debug("EOF on master_fd")
                            break

                        output_buffer, _ = self._handle_claude_output(data, output_buffer)
                    except OSError as e:
                        logging.error(f"Error reading from master_fd: {e}")
                        break

                # Check for idle state
                output_buffer = self._check_idle_state(output_buffer)

            # Wait for process to finish
            if self.process:
                self.process.wait()
                exit_code = self.process.returncode
                logging.info(f"Claude Code exited with code {exit_code}")
                return exit_code if exit_code is not None else 0

            return 0

        except Exception as e:
            logging.error(f"Unexpected error: {e}", exc_info=True)
            raise
        finally:
            self.cleanup()

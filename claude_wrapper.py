#!/usr/bin/env python3
"""
Claude Code Auto-Approve Wrapper - COMPLEX VERSION (with all safety checks)

A production-ready wrapper for Claude Code that automatically approves permission
prompts after a configurable countdown, while still presenting actual questions
for user input.

Features:
- Auto-approve permissions with countdown
- Cancel countdown with any key press
- Restart countdown with Ctrl+A
- Respects terminal size and resizing
- Configuration file support
- Comprehensive error handling
- Debug mode for troubleshooting

Author: Claude & User
License: MIT
"""

import sys
import os
import select
import threading
import time
import re
import signal
import struct
import fcntl
import json
import copy
import shutil
import logging
import hashlib
from pathlib import Path
from subprocess import Popen, PIPE, STDOUT
from typing import Optional, Dict, Any
import termios
import tty
import pty

# Version
__version__ = "1.1.0"

# Constants
DEFAULT_CONFIG_PATH = Path.home() / ".claude_wrapper.conf"
DEFAULT_LOG_PATH = Path.home() / ".claude_wrapper.log"
DEFAULT_AUTO_APPROVE_DELAY = 1
MAX_BUFFER_SIZE = 10000
READ_SIZE = 1024

# ANSI escape code patterns
ANSI_ESCAPE_PATTERN = re.compile(r'\x1b(?:[@-Z\\-_]|\[[0-?]*[ -/]*[@-~]|\][^\x07]*\x07)')
# Pattern for cursor movement codes specifically (should be replaced with space)
# Comprehensive pattern covering: A-G (cursor moves), H/f (positioning), J/K (erase), s/u (save/restore)
ANSI_CURSOR_PATTERN = re.compile(r'\x1b\[[\d;]*[ABCDEFGHJKfsu]')


class ClaudeWrapperError(Exception):
    """Base exception for Claude Wrapper errors"""
    pass


class ClaudeNotFoundError(ClaudeWrapperError):
    """Raised when Claude Code executable is not found"""
    pass


class TerminalSetupError(ClaudeWrapperError):
    """Raised when terminal setup fails"""
    pass


class Config:
    """Configuration management for Claude Wrapper"""

    DEFAULT_CONFIG = {
        "auto_approve_delay": DEFAULT_AUTO_APPROVE_DELAY,
        "debug": False,
        "log_file": str(DEFAULT_LOG_PATH),
        "claude_path": "claude",
        "auto_approve_enabled": True,
        "show_status_bar": True,  # Show status bar with countdown and counter
        "toggle_key": "\x01",  # Ctrl+A (toggle auto-approve on/off)
        "cooldown_seconds": 1.0,  # Cooldown for same prompt (different prompts approved immediately)
        "min_detection_score": 3,  # Minimum score required to detect permission prompt (higher = stricter)
        "max_approvals_per_minute": 500,  # Maximum total approvals per minute (very high for batch operations)
        "max_same_prompt_approvals": 5,  # Maximum times to approve the SAME prompt in 60s (loop detection)
        "idle_detection_enabled": True,  # Enable idle detection fallback
        "idle_timeout_seconds": 5.0,  # Seconds of no output before triggering idle action
        "patterns": {
            "permission_indicators": [
                # Don't include indicators that are already checked explicitly in code
                # to avoid double-counting in the scoring system
            ],
            "text_input_indicators": [
                r"Type.*yes",
                r"Enter.*yes",
                r"\(y/n\)"
            ]
        }
    }

    def __init__(self, config_path: Optional[Path] = None):
        self.config_path = config_path or DEFAULT_CONFIG_PATH
        self.config = self._load_config()

    def _deep_merge(self, base: Dict[str, Any], override: Dict[str, Any]) -> Dict[str, Any]:
        """Recursively merge override into base, preserving nested structures"""
        result = copy.deepcopy(base)
        for key, value in override.items():
            if isinstance(value, dict) and key in result and isinstance(result[key], dict):
                result[key] = self._deep_merge(result[key], value)
            else:
                result[key] = value
        return result

    def _load_config(self) -> Dict[str, Any]:
        """Load configuration from file or use defaults"""
        if self.config_path.exists():
            try:
                with open(self.config_path, 'r') as f:
                    user_config = json.load(f)
                    # Use deep merge to handle nested dictionaries properly
                    config = self._deep_merge(self.DEFAULT_CONFIG, user_config)
                    return config
            except Exception as e:
                logging.warning(f"Failed to load config from {self.config_path}: {e}")
                return copy.deepcopy(self.DEFAULT_CONFIG)
        return copy.deepcopy(self.DEFAULT_CONFIG)

    def save_config(self):
        """Save current configuration to file"""
        try:
            with open(self.config_path, 'w') as f:
                json.dump(self.config, f, indent=2)
        except Exception as e:
            logging.error(f"Failed to save config to {self.config_path}: {e}")

    def get(self, key: str, default: Any = None) -> Any:
        """Get configuration value"""
        return self.config.get(key, default)

    def set(self, key: str, value: Any):
        """Set configuration value"""
        self.config[key] = value

class ClaudeWrapper:
    """Main wrapper class for Claude Code with auto-approval functionality"""

    def __init__(self, config: Config):
        self.config = config
        self.auto_approve_delay = config.get("auto_approve_delay", DEFAULT_AUTO_APPROVE_DELAY)
        self.debug = config.get("debug", False)
        self.claude_path = config.get("claude_path", "claude")
        self.auto_approve_enabled = config.get("auto_approve_enabled", True)

        # State
        self.master_fd: Optional[int] = None
        self.process: Optional[Popen] = None
        self.original_tty: Optional[list] = None
        self._countdown_running = False
        self._countdown_cancelled = threading.Event()
        self._countdown_approve_now = threading.Event()
        self._countdown_thread: Optional[threading.Thread] = None
        self.last_buffer = ""
        self._shutdown = threading.Event()
        self._countdown_lock = threading.Lock()  # Thread safety for countdown state
        self._auto_approve_count = 0  # Track how many times auto-approve has been triggered
        self._last_approval_time = 0  # Track when we last approved to prevent rapid re-triggers
        self._approved_prompt_hash = None  # Hash of the last approved prompt to avoid duplicates
        self._approval_timestamps = []  # Track recent approval times for rate limiting
        self._last_output_time = time.time()  # Track when we last received output
        self._last_idle_action_time = 0  # Track when we last performed an idle action

        # Terminal layout
        self.term_height = 24  # Default, will be updated
        self.term_width = 80   # Default, will be updated
        self.status_bar_row = 24  # Bottom row for status
        self.content_rows = 23  # Rows available for Claude output

        # Setup logging
        self._setup_logging()

    def _setup_logging(self):
        """Configure logging"""
        if self.debug:
            log_file = self.config.get("log_file", str(DEFAULT_LOG_PATH))
            logging.basicConfig(
                level=logging.DEBUG,
                format='%(asctime)s - %(levelname)s - %(message)s',
                handlers=[
                    logging.FileHandler(log_file),
                    logging.StreamHandler(sys.stderr)
                ]
            )
            logging.info("Debug mode enabled")
        else:
            logging.basicConfig(level=logging.ERROR)

    def _debug_log(self, message: str):
        """Write debug message to configured log file"""
        if self.debug:
            log_file = self.config.get("log_file", str(DEFAULT_LOG_PATH))
            try:
                with open(log_file, 'a') as f:
                    f.write(message)
            except Exception as e:
                logging.error(f"Failed to write debug log: {e}")

    def strip_ansi(self, text: str) -> str:
        """Remove ANSI escape codes from text, replacing cursor movements with spaces"""
        # First replace cursor movement codes with spaces
        text = ANSI_CURSOR_PATTERN.sub(' ', text)
        # Then remove all other ANSI codes
        text = ANSI_ESCAPE_PATTERN.sub('', text)
        # Clean up multiple spaces
        text = re.sub(r' +', ' ', text)
        return text

    def is_permission_prompt(self, text: str) -> bool:
        """Detect if text is a permission prompt or approval request

        Uses strict scoring system to avoid false positives.
        Requires multiple indicators to be present simultaneously.
        """
        # Strip ANSI codes for cleaner matching
        clean_text = self.strip_ansi(text)

        # Use a scoring system - need minimum score to be considered a prompt
        score = 0
        min_score = self.config.get("min_detection_score", 3)  # Configurable threshold

        # Strong indicators (worth 2 points each)
        if 'Permission rule' in clean_text:
            score += 2
        if 'Do you want to proceed?' in clean_text:
            score += 2
        if 'Would you like to proceed?' in clean_text:
            score += 2
        # File creation/modification prompts
        if re.search(r'Do you want to (create|edit|delete|modify|write)', clean_text, re.IGNORECASE):
            score += 2
        if re.search(r'Would you like to (create|edit|delete|modify|write)', clean_text, re.IGNORECASE):
            score += 2

        # Medium indicators (worth 1 point each)
        if 'Esc to cancel' in clean_text:
            score += 1
        if 'Tab to amend' in clean_text:
            score += 1
        if 'Enter to approve' in clean_text or 'Enter to confirm' in clean_text:
            score += 1

        # Action buttons - must have both Yes AND No (not just one)
        # This is a strong indicator of permission prompts (worth 3 points - enough on its own)
        has_yes = '1. Yes' in clean_text or '1) Yes' in clean_text
        # No can be option 2 or 3 (depending on prompt type)
        has_no = re.search(r'[23]\.\s*No', clean_text) or re.search(r'[23]\)\s*No', clean_text)
        if has_yes and has_no:
            score += 3

        # y/n prompt format
        if re.search(r'\(y/n\)\s*$', clean_text, re.MULTILINE):
            score += 1

        # Check configured permission indicators (worth 2 points)
        patterns_config = self.config.get("patterns", {})
        permission_indicators = patterns_config.get("permission_indicators", [])
        for indicator in permission_indicators:
            if indicator in clean_text:
                score += 2
                break  # Only count once

        # Additional safety checks
        # If text contains code blocks, it's likely not a permission prompt
        if '```' in clean_text:
            score = 0  # Reset score

        # If text is too long, it's probably conversation, not a prompt
        # Real permission prompts are usually < 1000 characters
        if len(clean_text) > 2000:
            if self.debug:
                self._debug_log(
                    f"\n=== Text too long to be permission prompt ===\n"
                    f"Length: {len(clean_text)}\n"
                )
            score = max(0, score - 2)  # Penalize long text

        # If there are many sentences, it's probably not a prompt
        sentence_count = clean_text.count('.') + clean_text.count('?') + clean_text.count('!')
        if sentence_count > 10:
            score = max(0, score - 1)

        if self.debug:
            self._debug_log(
                f"\n=== Permission Detection Score ===\n"
                f"Score: {score}/{min_score} required\n"
                f"Length: {len(clean_text)}\n"
                f"Sentences: {sentence_count}\n"
                f"Has 'Permission rule': {'Permission rule' in clean_text}\n"
                f"Has 'Do you want to proceed': {'Do you want to proceed' in clean_text}\n"
                f"Has 'Yes/No options': {has_yes and has_no}\n"
                f"Last 200 chars: {repr(clean_text[-200:])}\n"
            )

        # If score is close but not quite enough, log a warning
        if self.debug and score == min_score - 1:
            self._debug_log(
                f"\n=== WARNING: Score just below threshold ===\n"
                f"Score: {score}, need {min_score}\n"
                f"This might be a missed permission prompt!\n"
                f"Full text: {repr(clean_text)}\n"
            )

        return score >= min_score

    def get_prompt_type(self, text: str) -> str:
        """Determine what type of prompt this is"""
        clean_text = self.strip_ansi(text)

        # Check if it's a numbered menu
        if '1. Yes' in clean_text or '2. No' in clean_text:
            return 'numbered_menu'

        # Check if it's a text input prompt
        if re.search(r'Type.*yes|Enter.*yes|\(y/n\)', clean_text, re.IGNORECASE):
            return 'text_input'

        # Default to numbered menu (most common)
        return 'numbered_menu'

    def is_question_prompt(self, text: str) -> bool:
        """Detect if text is an actual question (not a permission)"""
        # Questions from AskUserQuestion usually have multiple choice options
        # and don't match permission patterns
        if self.is_permission_prompt(text):
            return False

        question_patterns = [
            r'\d+\)\s+',  # Numbered options like "1) Option A"
            r'Select an option',
            r'Choose.*:',
            r'\[.*\].*\?',  # [Option] format
        ]

        for pattern in question_patterns:
            if re.search(pattern, text, re.IGNORECASE):
                return True
        return False

    def draw_status_bar(self, message, color_code="33"):
        """Draw a message in the status bar area"""
        # Skip if status bar is disabled
        if not self.config.get("show_status_bar", True):
            return

        try:
            # Save cursor position
            sys.stderr.write(f"\0337")

            # Move to status bar area (outside scrolling region)
            sys.stderr.write(f"\033[{self.status_bar_row};1H")

            # Clear the status bar area
            for row in range(self.status_bar_row, self.term_height + 1):
                sys.stderr.write(f"\033[{row};1H\033[K")

            # Draw top border of status bar
            sys.stderr.write(f"\033[{self.status_bar_row};1H")
            sys.stderr.write(f"\033[2m{'─' * self.term_width}\033[0m")

            # Draw message on next line
            sys.stderr.write(f"\033[{self.status_bar_row + 1};1H")
            sys.stderr.write(f"\033[{color_code}m{message}\033[0m")

            # Restore cursor position
            sys.stderr.write(f"\0338")
            sys.stderr.flush()
        except Exception as e:
            logging.debug(f"Failed to draw status bar (likely terminal resize): {e}")
            # Try to restore cursor at least
            try:
                sys.stderr.write(f"\0338")
                sys.stderr.flush()
            except:
                pass

    def clear_status_bar(self):
        """Show idle state in status bar (don't actually clear to avoid jumps)"""
        if self.auto_approve_enabled:
            if self._auto_approve_count > 0:
                self.draw_status_bar(f"Ready (auto-approve ON, {self._auto_approve_count} executed) [Ctrl+A to toggle]", "2")
            else:
                self.draw_status_bar("Ready (auto-approve ON) [Ctrl+A to toggle]", "2")
        else:
            self.draw_status_bar("Ready (auto-approve OFF) [Ctrl+A to toggle]", "90")

    def countdown_and_approve(self, seconds):
        """Show countdown and auto-approve, cancellable by user input"""
        # Use dedicated status bar area for countdown display

        for i in range(seconds, 0, -1):
            # Check if user wants to approve immediately
            if self._countdown_approve_now.is_set():
                break  # Exit countdown and proceed to approval

            # Check if cancelled
            if self._countdown_cancelled.is_set():
                self.draw_status_bar("✗ Auto-approve cancelled", "90")
                time.sleep(0.3)
                self.clear_status_bar()
                return

            # Draw countdown message
            self.draw_status_bar(
                f"⏱  Auto-approving in {i}s... (Enter=approve now, any key=cancel, Ctrl+A=toggle off)",
                "33"
            )
            time.sleep(1)

        # Final check before approving
        if self._countdown_cancelled.is_set():
            self.draw_status_bar("✗ Auto-approve cancelled", "90")
            time.sleep(0.3)
            self.clear_status_bar()
            return

        # Increment counter and record approval time
        self._auto_approve_count += 1
        self._last_approval_time = time.time()
        self._approval_timestamps.append(self._last_approval_time)  # Track for rate limiting

        # Show approval message (either from timeout or immediate approval)
        if not self._countdown_approve_now.is_set():
            self.draw_status_bar(f"✓ Auto-approved (#{self._auto_approve_count})", "32")
            time.sleep(0.3)
        else:
            self.draw_status_bar(f"✓ Approved immediately (#{self._auto_approve_count})", "32")
            time.sleep(0.3)

        # Determine what to send based on prompt type
        prompt_type = self.get_prompt_type(self.last_buffer)

        if self.debug:
            self._debug_log(
                f"\n=== Sending approval ===\n"
                f"Prompt type: {prompt_type}\n"
                f"Writing to fd: {self.master_fd}\n"
            )

        time.sleep(0.1)  # Small delay to ensure prompt is ready

        if prompt_type == 'numbered_menu':
            # For numbered menu, just press Enter (cursor is already on "Yes" by default)
            bytes_written = os.write(self.master_fd, b'\r')
            if self.debug:
                self._debug_log(f"Wrote {bytes_written} bytes (Enter key for numbered menu)\n")
        else:
            # Send 'yes' for text input prompts
            bytes_written = os.write(self.master_fd, b'yes')
            if self.debug:
                self._debug_log(f"Wrote {bytes_written} bytes (text 'yes')\n")

            time.sleep(0.1)

            # Send Enter
            bytes_written = os.write(self.master_fd, b'\r')
            if self.debug:
                self._debug_log(f"Wrote {bytes_written} bytes (Enter key after 'yes')\n")

        # Return to ready state after sending approval
        self.clear_status_bar()

    def handle_output(self, buffer):
        """Process output buffer and detect prompts"""
        # Store buffer for later prompt type detection
        self.last_buffer = buffer

        if self.debug:
            # Write debug info to a log file instead of stdout
            clean_text = self.strip_ansi(buffer)
            self._debug_log(
                f"\n=== Buffer Check ===\n"
                f"Buffer length: {len(buffer)}\n"
                f"Last 200 chars raw: {repr(buffer[-200:])}\n"
                f"Last 200 chars clean: {repr(clean_text[-200:])}\n"
                f"Contains 'Permission rule': {'Permission rule' in clean_text}\n"
                f"Contains 'Do you want to proceed': {'Do you want to proceed' in clean_text}\n"
                f"Contains 'Esc to cancel': {'Esc to cancel' in clean_text}\n"
                f"Is permission prompt: {self.is_permission_prompt(buffer)}\n"
                f"Prompt type: {self.get_prompt_type(buffer)}\n"
            )

        # Check if it's a permission prompt
        if self.is_permission_prompt(buffer):
            current_time = time.time()
            cooldown_seconds = self.config.get("cooldown_seconds", 1.0)
            time_since_last = current_time - self._last_approval_time

            # Rate limiting: check if we're approving too many prompts too quickly
            # Remove old timestamps (older than 60 seconds)
            self._approval_timestamps = [t for t in self._approval_timestamps if current_time - t < 60]

            # Calculate hash to identify this specific prompt
            prompt_hash = hashlib.md5(self.strip_ansi(buffer).encode()).hexdigest()

            # Smart rate limiting: only block if we're seeing the SAME prompt repeatedly
            # Count how many times we've approved THIS SPECIFIC prompt in the last 60 seconds
            if not hasattr(self, '_approval_hashes'):
                self._approval_hashes = []

            # Remove old hash entries (older than 60 seconds)
            self._approval_hashes = [(h, t) for h, t in self._approval_hashes if current_time - t < 60]

            # Count approvals of THIS specific prompt
            same_prompt_count = sum(1 for h, t in self._approval_hashes if h == prompt_hash)

            # If we've approved the same prompt more than X times in 60 seconds, it's a loop
            max_same_prompt_approvals = self.config.get("max_same_prompt_approvals", 5)
            if same_prompt_count >= max_same_prompt_approvals:
                if self.debug:
                    self._debug_log(
                        f"\n=== SAME PROMPT LOOP DETECTED ===\n"
                        f"Same prompt approved {same_prompt_count} times in 60s\n"
                        f"Max allowed: {max_same_prompt_approvals}\n"
                        f"This is likely a bug - blocking further approvals\n"
                    )
                # Show warning to user
                self.draw_status_bar(
                    f"⚠  Same prompt loop detected ({same_prompt_count}x) - blocked",
                    "31"
                )
                time.sleep(2)
                return False

            # Global rate limit as final safety (very high limit for different prompts)
            max_approvals_per_minute = self.config.get("max_approvals_per_minute", 500)
            if len(self._approval_timestamps) >= max_approvals_per_minute:
                if self.debug:
                    self._debug_log(
                        f"\n=== GLOBAL RATE LIMIT EXCEEDED ===\n"
                        f"Total approvals in last 60s: {len(self._approval_timestamps)}\n"
                        f"Max allowed: {max_approvals_per_minute}\n"
                    )
                # Show warning to user
                self.draw_status_bar(
                    f"⚠  Rate limit: {len(self._approval_timestamps)} approvals/min (paused)",
                    "31"
                )
                time.sleep(2)
                return False

            # Check if it's the same as the last approved prompt
            is_same_prompt = (prompt_hash == self._approved_prompt_hash and self._approved_prompt_hash is not None)

            # Only apply cooldown if it's the SAME prompt (prevent duplicate approvals)
            # Different prompts can be approved immediately even within cooldown period
            if is_same_prompt and time_since_last < cooldown_seconds:
                if self.debug:
                    self._debug_log(
                        f"\n=== Duplicate prompt blocked ===\n"
                        f"Same prompt within cooldown: {time_since_last:.2f}s\n"
                        f"Skipping auto-approve\n"
                    )
                # Show user why it was skipped
                self.draw_status_bar(f"Duplicate prompt (blocked)", "90")
                return False

            # Additional check: if it's the same prompt and within extended window, still block
            # This handles cases where buffer contains old prompt text
            hash_expiry = cooldown_seconds * 3
            if is_same_prompt and time_since_last < hash_expiry:
                if self.debug:
                    self._debug_log(
                        f"\n=== Duplicate prompt (extended window) ===\n"
                        f"Prompt hash: {prompt_hash}\n"
                        f"Time since last: {time_since_last:.2f}s\n"
                        f"Skipping auto-approve\n"
                    )
                # Show user why it was skipped
                self.draw_status_bar(f"Same prompt as before (blocked)", "90")
                time.sleep(1)
                self.clear_status_bar()
                return False

            # This is either a new prompt or enough time has passed
            # Store this prompt hash for duplicate detection
            self._approved_prompt_hash = prompt_hash

            if self.debug and not is_same_prompt:
                self._debug_log(
                    f"\n=== New prompt detected ===\n"
                    f"Different from last prompt\n"
                    f"Time since last approval: {time_since_last:.2f}s\n"
                    f"Proceeding with auto-approve\n"
                )

            # Use lock to prevent race condition when multiple prompts appear quickly
            with self._countdown_lock:
                # If a countdown is running for a different prompt, cancel it and start new one
                if self._countdown_running and not is_same_prompt:
                    if self.debug:
                        self._debug_log("\n=== Cancelling old countdown for new prompt ===\n")
                    self._countdown_cancelled.set()
                    # Force reset the flag so we can start a new countdown
                    self._countdown_running = False

                if not self._countdown_running:
                    self._countdown_running = True
                    self._countdown_cancelled.clear()  # Reset cancellation flag
                    self._countdown_approve_now.clear()  # Reset immediate approval flag

                    # Track this prompt hash for loop detection (only when actually approving)
                    if not hasattr(self, '_approval_hashes'):
                        self._approval_hashes = []
                    self._approval_hashes.append((prompt_hash, current_time))

                    def countdown_wrapper():
                        try:
                            self.countdown_and_approve(self.auto_approve_delay)
                        finally:
                            # Always reset flag, even if exception occurs
                            with self._countdown_lock:
                                self._countdown_running = False

                    # Start countdown in a separate thread
                    self._countdown_thread = threading.Thread(target=countdown_wrapper)
                    self._countdown_thread.daemon = True
                    self._countdown_thread.start()
                    return True

        return False

    def cancel_countdown(self):
        """Cancel any running countdown"""
        if self._countdown_running:
            self._countdown_cancelled.set()
            # Clear the prompt hash so user can manually interact
            self._approved_prompt_hash = None
            if self.debug:
                self._debug_log("\n=== Countdown cancelled by user input ===\n")

    def set_terminal_size(self):
        """Set the PTY terminal size to match the current terminal"""
        if not sys.stdout.isatty():
            return

        try:
            # Get current terminal size
            size = struct.unpack('HHHH', fcntl.ioctl(sys.stdout.fileno(), termios.TIOCGWINSZ, struct.pack('HHHH', 0, 0, 0, 0)))
            rows, cols = size[0], size[1]

            # Update terminal dimensions
            self.term_height = rows
            self.term_width = cols

            # Calculate split: reserve 2 rows for status bar (minimum)
            # For small terminals (< 20 rows), use 2 rows; for larger, could use more
            status_bar_height = 2
            if rows < 10:
                # Very small terminal - minimal status area
                status_bar_height = 1

            self.content_rows = max(1, rows - status_bar_height)
            self.status_bar_row = self.content_rows + 1

            # Set PTY size (rows, cols, xpixel, ypixel)
            fcntl.ioctl(self.master_fd, termios.TIOCSWINSZ, struct.pack('HHHH', *size))

            # Set up scrolling region: top to content_rows
            # This keeps the status bar fixed at the bottom
            # Only set scrolling region if status bar is enabled
            if self.config.get("show_status_bar", True):
                sys.stdout.write(f"\033[1;{self.content_rows}r")
                sys.stdout.flush()

            if self.debug:
                self._debug_log(
                    f"\n=== Terminal size set ===\n"
                    f"Rows: {rows}, Cols: {cols}\n"
                    f"Content rows: {self.content_rows}\n"
                    f"Status bar row: {self.status_bar_row}\n"
                )
        except Exception as e:
            if self.debug:
                self._debug_log(f"\n=== Error setting terminal size ===\n{e}\n")

    def handle_sigwinch(self, signum, frame):
        """Handle terminal resize events"""
        self.set_terminal_size()

    def _check_claude_exists(self) -> bool:
        """Verify that Claude Code executable exists"""
        return shutil.which(self.claude_path) is not None

    def _validate_terminal(self):
        """Validate terminal requirements"""
        if not sys.stdin.isatty():
            raise TerminalSetupError("stdin is not a TTY")
        if not sys.stdout.isatty():
            raise TerminalSetupError("stdout is not a TTY")

    def cleanup(self):
        """Cleanup resources and restore terminal state"""
        logging.debug("Cleaning up...")

        # Cancel any running countdown and wait for it to finish
        if self._countdown_running:
            self._countdown_cancelled.set()
            if self._countdown_thread and self._countdown_thread.is_alive():
                logging.debug("Waiting for countdown thread to finish...")
                self._countdown_thread.join(timeout=2.0)
                if self._countdown_thread.is_alive():
                    logging.warning("Countdown thread did not finish in time")

        # Clear status bar area before exiting
        try:
            # Move to status bar row and clear from there to end of screen
            sys.stderr.write(f"\033[{self.status_bar_row};1H")
            sys.stderr.write("\033[J")  # Clear from cursor to end of screen
            sys.stderr.flush()
        except Exception as e:
            logging.debug(f"Failed to clear status bar: {e}")

        # Reset scrolling region to full screen
        try:
            sys.stdout.write("\033[r")
            sys.stdout.flush()
        except Exception as e:
            logging.error(f"Failed to reset terminal display: {e}")

        # Restore terminal settings
        if self.original_tty and sys.stdin.isatty():
            try:
                termios.tcsetattr(sys.stdin, termios.TCSADRAIN, self.original_tty)
                logging.debug("Terminal settings restored")
            except Exception as e:
                logging.error(f"Failed to restore terminal: {e}")

        # Close master fd
        if self.master_fd:
            try:
                os.close(self.master_fd)
                logging.debug("Master FD closed")
            except Exception as e:
                logging.error(f"Failed to close master fd: {e}")

        # Terminate process if still running
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

    def handle_shutdown_signal(self, signum, frame):
        """Handle shutdown signals gracefully"""
        logging.info(f"Received signal {signum}, shutting down...")
        self._shutdown.set()
        self.cleanup()
        sys.exit(128 + signum)

    def run(self, args=None):
        """Run Claude Code with the wrapper"""
        # Validate environment
        if not self._check_claude_exists():
            raise ClaudeNotFoundError(
                f"Claude Code executable '{self.claude_path}' not found in PATH. "
                "Please install Claude Code or specify the correct path in config."
            )

        try:
            self._validate_terminal()
        except TerminalSetupError as e:
            logging.error(f"Terminal validation failed: {e}")
            raise

        # Build command
        cmd = [self.claude_path]
        if args:
            cmd.extend(args)

        logging.info(f"Starting Claude Code: {' '.join(cmd)}")

        # Save original terminal settings
        if sys.stdin.isatty():
            self.original_tty = termios.tcgetattr(sys.stdin)

        # Setup signal handlers
        signal.signal(signal.SIGINT, self.handle_shutdown_signal)
        signal.signal(signal.SIGTERM, self.handle_shutdown_signal)
        signal.signal(signal.SIGHUP, self.handle_shutdown_signal)

        try:
            # Create pseudo-terminal
            self.master_fd, slave_fd = pty.openpty()

            # Start Claude process
            self.process = Popen(
                cmd,
                stdin=slave_fd,
                stdout=slave_fd,
                stderr=slave_fd,
                close_fds=True
            )

            os.close(slave_fd)

            # Clear screen and reset cursor position
            # This prevents old terminal content from showing in the middle
            sys.stdout.write("\033[2J")  # Clear entire screen
            sys.stdout.write("\033[H")   # Move cursor to home position
            sys.stdout.flush()

            # Set initial terminal size
            self.set_terminal_size()

            # Initialize status bar (if enabled)
            if self.auto_approve_enabled and self.config.get("show_status_bar", True):
                self.clear_status_bar()

            # Set up signal handler for terminal resize
            signal.signal(signal.SIGWINCH, self.handle_sigwinch)

            # Set terminal to raw mode for pass-through
            if sys.stdin.isatty():
                tty.setraw(sys.stdin.fileno())

            output_buffer = ""

            while not self._shutdown.is_set():
                # Check if process is still alive
                if self.process.poll() is not None:
                    logging.debug("Process exited")
                    break

                # Use select to monitor both stdin and master_fd
                try:
                    r, w, e = select.select([sys.stdin, self.master_fd], [], [], 0.1)
                except select.error as e:
                    if e.args[0] == 4:  # EINTR - interrupted by signal
                        continue
                    logging.error(f"Select error: {e}")
                    break

                # Handle input from user
                if sys.stdin in r:
                    try:
                        char = os.read(sys.stdin.fileno(), READ_SIZE)
                        if not char:
                            logging.debug("EOF on stdin")
                            break

                        # Check if Enter pressed during countdown - approve immediately
                        if self._countdown_running and char in (b'\r', b'\n'):
                            self._countdown_approve_now.set()
                            continue  # Don't forward Enter to Claude, wait for countdown thread to approve

                        # Check for special commands
                        toggle_key = self.config.get("toggle_key", "\x01").encode()
                        if char == toggle_key:
                            # Toggle auto-approve on/off
                            self.auto_approve_enabled = not self.auto_approve_enabled

                            # Cancel any running countdown
                            if self._countdown_running:
                                self.cancel_countdown()

                            # Show toggle message
                            if self.auto_approve_enabled:
                                self.draw_status_bar("✓ Auto-approve ENABLED", "32")
                            else:
                                self.draw_status_bar("✗ Auto-approve DISABLED", "31")

                            time.sleep(0.8)  # Show message longer
                            self.clear_status_bar()

                            if self.debug:
                                self._debug_log(
                                    f"\n=== Auto-approve toggled ===\n"
                                    f"New state: {'ENABLED' if self.auto_approve_enabled else 'DISABLED'}\n"
                                )

                            continue  # Don't forward toggle key to Claude

                        # Cancel countdown if user presses any other key
                        if self._countdown_running:
                            self.cancel_countdown()

                        # Forward the input to Claude
                        os.write(self.master_fd, char)
                    except OSError as e:
                        logging.error(f"Error reading from stdin: {e}")
                        break

                # Handle output from Claude
                if self.master_fd in r:
                    try:
                        data = os.read(self.master_fd, READ_SIZE)
                        if not data:
                            logging.debug("EOF on master_fd")
                            break

                        # Update last output time
                        self._last_output_time = time.time()

                        # Write to stdout
                        os.write(sys.stdout.fileno(), data)

                        # Add to buffer for prompt detection (only if auto-approve is enabled)
                        if self.auto_approve_enabled:
                            try:
                                decoded = data.decode('utf-8', errors='replace')
                                output_buffer += decoded
                            except Exception as e:
                                logging.warning(f"Failed to decode output data: {e}")
                                # Continue without this chunk
                                pass

                            # Keep buffer size manageable - trim from the start but keep reasonable context
                            if len(output_buffer) > MAX_BUFFER_SIZE:
                                # Keep last 80% to maintain context for prompt detection
                                trim_point = int(MAX_BUFFER_SIZE * 0.2)
                                output_buffer = output_buffer[trim_point:]

                            # Check for prompts
                            was_detected = self.handle_output(output_buffer)

                            # If we detected and started handling a prompt, clear the buffer
                            # This prevents re-detecting the same prompt from old buffer content
                            if was_detected:
                                output_buffer = ""
                                if self.debug:
                                    self._debug_log("\n=== Buffer cleared after prompt detection ===\n")

                    except OSError as e:
                        logging.error(f"Error reading from master_fd: {e}")
                        break

                # Check for idle state (no output for a while)
                if (self.config.get("idle_detection_enabled", True) and
                    self.auto_approve_enabled and
                    not self._countdown_running):

                    current_time = time.time()
                    idle_timeout = self.config.get("idle_timeout_seconds", 5.0)
                    time_since_output = current_time - self._last_output_time
                    time_since_idle_action = current_time - self._last_idle_action_time

                    # Debug log idle state periodically (every 2 seconds of idle time)
                    if self.debug and time_since_output >= 2.0 and int(time_since_output) % 2 == 0:
                        if not hasattr(self, '_last_idle_log_time') or current_time - self._last_idle_log_time >= 2.0:
                            self._debug_log(
                                f"\n=== Idle state check ===\n"
                                f"Time since output: {time_since_output:.2f}s\n"
                                f"Time since idle action: {time_since_idle_action:.2f}s\n"
                                f"Buffer length: {len(output_buffer)}\n"
                                f"Timeout threshold: {idle_timeout}s\n"
                            )
                            self._last_idle_log_time = current_time

                    # If idle for long enough and not recently acted
                    if time_since_output >= idle_timeout and time_since_idle_action >= idle_timeout:
                        # Check if the current buffer looks like a prompt we should handle
                        # Use a lower threshold for idle detection to catch edge cases
                        if len(output_buffer) > 50:  # Only act if there's meaningful output
                            if self.debug:
                                self._debug_log(
                                    f"\n=== Idle detection triggered ===\n"
                                    f"Time since output: {time_since_output:.2f}s\n"
                                    f"Buffer length: {len(output_buffer)}\n"
                                    f"Attempting approval\n"
                                )

                            self.draw_status_bar("⏱  Idle detected, auto-approving...", "33")
                            time.sleep(0.3)

                            # Use the same approval logic as countdown_and_approve
                            # Determine what to send based on prompt type in buffer
                            prompt_type = self.get_prompt_type(output_buffer)

                            if self.debug:
                                self._debug_log(
                                    f"Prompt type detected: {prompt_type}\n"
                                )

                            time.sleep(0.1)  # Small delay to ensure prompt is ready

                            if prompt_type == 'numbered_menu':
                                # For numbered menu, just press Enter (cursor should be on "Yes" by default)
                                bytes_written = os.write(self.master_fd, b'\r')
                                if self.debug:
                                    self._debug_log(f"Wrote {bytes_written} bytes (Enter key for numbered menu)\n")
                            else:
                                # Send 'yes' for text input prompts
                                bytes_written = os.write(self.master_fd, b'yes')
                                if self.debug:
                                    self._debug_log(f"Wrote {bytes_written} bytes (text 'yes')\n")
                                time.sleep(0.1)
                                # Send Enter
                                bytes_written = os.write(self.master_fd, b'\r')
                                if self.debug:
                                    self._debug_log(f"Wrote {bytes_written} bytes (Enter key after 'yes')\n")

                            # Update counters
                            self._last_idle_action_time = current_time
                            self._auto_approve_count += 1
                            self._last_approval_time = current_time
                            self._approval_timestamps.append(current_time)

                            self.draw_status_bar(f"✓ Auto-approved via idle detection (#{self._auto_approve_count})", "32")
                            time.sleep(0.3)
                            self.clear_status_bar()

                            # Clear buffer after handling
                            output_buffer = ""

            # Wait for process to finish
            if self.process:
                self.process.wait()
                exit_code = self.process.returncode
                logging.info(f"Claude Code exited with code {exit_code}")
                return exit_code
            return 0

        except Exception as e:
            logging.error(f"Unexpected error: {e}", exc_info=True)
            raise
        finally:
            self.cleanup()


def main():
    """Main entry point"""
    import argparse

    parser = argparse.ArgumentParser(
        prog='claude-wrapper',
        description='Production-ready wrapper for Claude Code with auto-approve functionality',
        epilog=f'Version {__version__} - Config file: {DEFAULT_CONFIG_PATH}',
        formatter_class=argparse.RawDescriptionHelpFormatter
    )

    parser.add_argument(
        '--version',
        action='version',
        version=f'%(prog)s {__version__}'
    )

    parser.add_argument(
        '--delay',
        type=int,
        metavar='SECONDS',
        help=f'Seconds to wait before auto-approving (default: from config or {DEFAULT_AUTO_APPROVE_DELAY})'
    )

    parser.add_argument(
        '--no-auto-approve',
        action='store_true',
        help='Disable auto-approve (just wrap Claude without auto-approval)'
    )

    parser.add_argument(
        '--no-status-bar',
        action='store_true',
        help='Disable status bar display (cleaner output)'
    )

    parser.add_argument(
        '--debug',
        action='store_true',
        help=f'Enable debug logging (default log: {DEFAULT_LOG_PATH})'
    )

    parser.add_argument(
        '--config',
        type=Path,
        metavar='PATH',
        help=f'Path to config file (default: {DEFAULT_CONFIG_PATH})'
    )

    parser.add_argument(
        '--init-config',
        action='store_true',
        help='Create default configuration file and exit'
    )

    parser.add_argument(
        '--claude-path',
        metavar='PATH',
        help='Path to Claude Code executable (default: claude)'
    )

    parser.add_argument(
        'claude_args',
        nargs='*',
        help='Arguments to pass to Claude Code'
    )

    args = parser.parse_args()

    # Load configuration
    config = Config(args.config)

    # Handle --init-config
    if args.init_config:
        config.save_config()
        print(f"Configuration file created at: {config.config_path}")
        print(f"Edit this file to customize behavior")
        sys.exit(0)

    # Override config with command-line arguments
    if args.delay is not None:
        config.set("auto_approve_delay", args.delay)

    if args.debug:
        config.set("debug", True)

    if args.claude_path:
        config.set("claude_path", args.claude_path)

    if args.no_auto_approve:
        config.set("auto_approve_enabled", False)

    if args.no_status_bar:
        config.set("show_status_bar", False)

    # Create wrapper
    wrapper = ClaudeWrapper(config)

    try:
        exit_code = wrapper.run(args.claude_args)
        sys.exit(exit_code if exit_code is not None else 0)

    except KeyboardInterrupt:
        sys.stderr.write("\n\n\033[33mInterrupted by user\033[0m\n")
        sys.exit(130)

    except ClaudeNotFoundError as e:
        sys.stderr.write(f"\n\033[31mError: {e}\033[0m\n")
        sys.exit(127)

    except TerminalSetupError as e:
        sys.stderr.write(f"\n\033[31mTerminal Error: {e}\033[0m\n")
        sys.exit(1)

    except Exception as e:
        sys.stderr.write(f"\n\033[31mUnexpected Error: {e}\033[0m\n")
        if config.get("debug"):
            import traceback
            traceback.print_exc()
        sys.exit(1)


if __name__ == '__main__':
    main()

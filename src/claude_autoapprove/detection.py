"""
Prompt detection and rate limiting for Claude Auto-Approve.

Contains logic for:
- Detecting permission prompts vs. questions
- Rate limiting to prevent runaway approvals
- Duplicate prompt detection
"""

import hashlib
import logging
import re
import time
from typing import Dict, List, Optional, Tuple

from .constants import ANSI_CURSOR_PATTERN, ANSI_ESCAPE_PATTERN


class PromptDetector:
    """
    Detects and classifies prompts from Claude Code.

    Uses a scoring system to distinguish permission prompts from
    actual questions and other output.

    Attributes:
        config: Configuration dictionary
        debug: Whether debug logging is enabled

    Example:
        >>> detector = PromptDetector(config, debug=True)
        >>> detector.is_permission_prompt("Do you want to proceed? 1. Yes 2. No")
        True
        >>> detector.get_prompt_type(text)
        'numbered_menu'
    """

    def __init__(self, config: Dict, debug: bool = False):
        """
        Initialize prompt detector.

        Args:
            config: Configuration dictionary
            debug: Enable debug logging
        """
        self.config = config
        self.debug = debug

    def strip_ansi(self, text: str) -> str:
        """
        Remove ANSI escape codes from text, replacing cursor movements with spaces.

        Args:
            text: Text with ANSI escape codes

        Returns:
            Clean text without ANSI codes

        Example:
            >>> detector.strip_ansi("\\x1b[32mHello\\x1b[0m")
            'Hello'
        """
        # First replace cursor movement codes with spaces
        text = ANSI_CURSOR_PATTERN.sub(" ", text)
        # Then remove all other ANSI codes
        text = ANSI_ESCAPE_PATTERN.sub("", text)
        # Clean up multiple spaces
        text = re.sub(r" +", " ", text)
        return text

    def is_permission_prompt(self, text: str) -> bool:
        """
        Detect if text is a permission prompt or approval request.

        Uses strict scoring system to avoid false positives.
        Requires multiple indicators to be present simultaneously.

        Args:
            text: Text to analyze

        Returns:
            True if text appears to be a permission prompt

        Example:
            >>> detector.is_permission_prompt("Do you want to proceed? 1. Yes 2. No")
            True
        """
        # Strip ANSI codes for cleaner matching
        clean_text = self.strip_ansi(text)

        # Use a scoring system - need minimum score to be considered a prompt
        score = 0
        min_score = self.config.get("min_detection_score", 3)

        # Strong indicators (worth 2 points each)
        if "Permission rule" in clean_text:
            score += 2
        if "Do you want to proceed?" in clean_text:
            score += 2
        if "Would you like to proceed?" in clean_text:
            score += 2

        # File creation/modification prompts
        if re.search(
            r"Do you want to (create|edit|delete|modify|write)", clean_text, re.IGNORECASE
        ):
            score += 2
        if re.search(
            r"Would you like to (create|edit|delete|modify|write)", clean_text, re.IGNORECASE
        ):
            score += 2

        # Medium indicators (worth 1 point each)
        if "Esc to cancel" in clean_text:
            score += 1
        if "Tab to amend" in clean_text:
            score += 1
        if "Enter to approve" in clean_text or "Enter to confirm" in clean_text:
            score += 1

        # Action buttons - must have both Yes AND No
        # This is a strong indicator of permission prompts (worth 3 points - enough on its own)
        has_yes = "1. Yes" in clean_text or "1) Yes" in clean_text
        has_no = re.search(r"[23]\.\s*No", clean_text) or re.search(r"[23]\)\s*No", clean_text)
        if has_yes and has_no:
            score += 3

        # y/n prompt format
        if re.search(r"\(y/n\)\s*$", clean_text, re.MULTILINE):
            score += 1

        # Check configured permission indicators (worth 2 points)
        patterns_config = self.config.get("patterns", {})
        permission_indicators = patterns_config.get("permission_indicators", [])
        for indicator in permission_indicators:
            if indicator in clean_text:
                score += 2
                break  # Only count once

        # Safety checks - reduce score for non-prompt-like content
        if "```" in clean_text:
            score = 0  # Code blocks are not permission prompts

        if len(clean_text) > 2000:
            if self.debug:
                logging.debug(f"Text too long to be permission prompt: {len(clean_text)} chars")
            score = max(0, score - 2)

        sentence_count = clean_text.count(".") + clean_text.count("?") + clean_text.count("!")
        if sentence_count > 10:
            score = max(0, score - 1)

        if self.debug:
            logging.debug(
                f"Permission Detection - Score: {score}/{min_score}, "
                f"Length: {len(clean_text)}, Sentences: {sentence_count}, "
                f"Has Permission rule: {'Permission rule' in clean_text}, "
                f"Has Yes/No: {has_yes and has_no}"
            )

        # Warn if score is just below threshold
        if self.debug and score == min_score - 1:
            logging.debug(
                f"WARNING: Score {score} just below threshold {min_score} - "
                f"possible missed prompt"
            )

        return score >= min_score

    def get_prompt_type(self, text: str) -> str:
        """
        Determine what type of prompt this is.

        Args:
            text: Text to analyze

        Returns:
            'numbered_menu' or 'text_input'

        Example:
            >>> detector.get_prompt_type("1. Yes  2. No")
            'numbered_menu'
        """
        clean_text = self.strip_ansi(text)

        # Check if it's a numbered menu
        if "1. Yes" in clean_text or "2. No" in clean_text:
            return "numbered_menu"

        # Check if it's a text input prompt
        if re.search(r"Type.*yes|Enter.*yes|\(y/n\)", clean_text, re.IGNORECASE):
            return "text_input"

        # Default to numbered menu (most common)
        return "numbered_menu"

    def is_question_prompt(self, text: str) -> bool:
        """
        Detect if text is an actual question (not a permission).

        Questions from AskUserQuestion usually have multiple choice options
        and don't match permission patterns.

        Args:
            text: Text to analyze

        Returns:
            True if text appears to be a question prompt

        Example:
            >>> detector.is_question_prompt("Select an option: 1) A  2) B")
            True
        """
        # Questions are not permissions
        if self.is_permission_prompt(text):
            return False

        question_patterns = [
            r"\d+\)\s+",  # Numbered options like "1) Option A"
            r"Select an option",
            r"Choose.*:",
            r"\[.*\].*\?",  # [Option] format
        ]

        for pattern in question_patterns:
            if re.search(pattern, text, re.IGNORECASE):
                return True
        return False


class RateLimiter:
    """
    Rate limiting and duplicate detection for auto-approvals.

    Prevents runaway approval loops by:
    - Limiting total approvals per minute
    - Limiting approvals of the same prompt
    - Tracking approval history

    Attributes:
        config: Configuration dictionary
        debug: Whether debug logging is enabled
        approval_timestamps: List of recent approval times
        approval_hashes: List of (hash, timestamp) tuples for duplicate detection

    Example:
        >>> limiter = RateLimiter(config, debug=True)
        >>> allowed, reason = limiter.check_approval_allowed("prompt text")
        >>> if allowed:
        ...     limiter.record_approval("prompt text")
    """

    def __init__(self, config: Dict, debug: bool = False):
        """
        Initialize rate limiter.

        Args:
            config: Configuration dictionary
            debug: Enable debug logging
        """
        self.config = config
        self.debug = debug
        self.approval_timestamps: List[float] = []
        self.approval_hashes: List[Tuple[str, float]] = []

    def cleanup_old_entries(self, window_seconds: int = 60) -> None:
        """
        Remove entries older than specified window.

        Args:
            window_seconds: Time window in seconds (default: 60)
        """
        current_time = time.time()
        self.approval_timestamps = [
            t for t in self.approval_timestamps if current_time - t < window_seconds
        ]
        self.approval_hashes = [
            (h, t) for h, t in self.approval_hashes if current_time - t < window_seconds
        ]

    def _hash_prompt(self, text: str) -> str:
        """
        Generate hash for prompt text.

        Args:
            text: Prompt text (should be ANSI-stripped)

        Returns:
            MD5 hash as hex string
        """
        return hashlib.md5(text.encode()).hexdigest()

    def check_approval_allowed(self, text: str, strip_ansi_func) -> Tuple[bool, Optional[str]]:
        """
        Check if approval is allowed based on rate limits.

        Args:
            text: Prompt text to check
            strip_ansi_func: Function to strip ANSI codes from text

        Returns:
            Tuple of (allowed: bool, reason: Optional[str])
            If not allowed, reason contains explanation

        Example:
            >>> allowed, reason = limiter.check_approval_allowed(text, detector.strip_ansi)
            >>> if not allowed:
            ...     print(f"Blocked: {reason}")
        """
        current_time = time.time()

        # Clean up old entries
        self.cleanup_old_entries()

        # Calculate hash to identify this specific prompt
        clean_text = strip_ansi_func(text)
        prompt_hash = self._hash_prompt(clean_text)

        # Check same-prompt limit
        same_prompt_count = sum(1 for h, t in self.approval_hashes if h == prompt_hash)
        max_same_prompt = self.config.get("max_same_prompt_approvals", 5)

        if same_prompt_count >= max_same_prompt:
            reason = (
                f"Same prompt approved {same_prompt_count} times in 60s (max: {max_same_prompt})"
            )
            if self.debug:
                logging.debug(f"SAME PROMPT LOOP DETECTED: {reason}")
            return False, reason

        # Check global rate limit
        max_per_minute = self.config.get("max_approvals_per_minute", 500)
        if len(self.approval_timestamps) >= max_per_minute:
            reason = (
                f"Rate limit: {len(self.approval_timestamps)} approvals/min (max: {max_per_minute})"
            )
            if self.debug:
                logging.debug(f"GLOBAL RATE LIMIT EXCEEDED: {reason}")
            return False, reason

        # Approval is allowed
        return True, None

    def record_approval(self, text: str, strip_ansi_func) -> None:
        """
        Record an approval for rate limiting.

        Args:
            text: Prompt text that was approved
            strip_ansi_func: Function to strip ANSI codes from text

        Example:
            >>> limiter.record_approval(text, detector.strip_ansi)
        """
        current_time = time.time()
        clean_text = strip_ansi_func(text)
        prompt_hash = self._hash_prompt(clean_text)

        self.approval_timestamps.append(current_time)
        self.approval_hashes.append((prompt_hash, current_time))

    def get_stats(self) -> Dict[str, int]:
        """
        Get current rate limiting statistics.

        Returns:
            Dictionary with stats: total_approvals, unique_prompts

        Example:
            >>> stats = limiter.get_stats()
            >>> print(f"Total: {stats['total_approvals']}")
        """
        self.cleanup_old_entries()
        unique_hashes = set(h for h, t in self.approval_hashes)
        return {
            "total_approvals": len(self.approval_timestamps),
            "unique_prompts": len(unique_hashes),
        }

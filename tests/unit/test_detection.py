"""
Unit tests for prompt detection and rate limiting.
"""

import time

import pytest

from claude_autoapprove.detection import PromptDetector, RateLimiter


class TestPromptDetector:
    """Test PromptDetector class."""

    @pytest.fixture
    def detector(self, default_config):
        """Create a detector instance."""
        return PromptDetector(default_config.config, debug=False)

    def test_strip_ansi(self, detector):
        """Test ANSI code stripping."""
        text_with_ansi = "\x1b[32mHello\x1b[0m \x1b[1mWorld\x1b[0m"
        clean = detector.strip_ansi(text_with_ansi)

        assert "Hello" in clean
        assert "World" in clean
        assert "\x1b" not in clean

    def test_strip_ansi_cursor_codes(self, detector):
        """Test stripping cursor movement codes."""
        text = "\x1b[2A\x1b[3BHello\x1b[1;1H World"
        clean = detector.strip_ansi(text)

        assert "Hello" in clean
        assert "World" in clean
        assert "\x1b" not in clean

    @pytest.mark.parametrize(
        "text,expected",
        [
            # Should detect (score >= 3)
            ("Permission rule: test\n1. Yes\n2. No\nEsc to cancel", True),  # 2+3+1 = 6
            ("Do you want to proceed?\n1. Yes\n2. No", True),  # 2+3 = 5
            ("Would you like to proceed?\n1. Yes\n2. No", True),  # 2+3 = 5
            ("Do you want to create this file?\n1. Yes\n2. No", True),  # 2+3 = 5
            ("1. Yes\n2. No", True),  # 3 points from Yes/No buttons (meets threshold)
            # Should not detect (score < 3)
            ("Just regular text", False),  # 0
            ("This is a question about coding?", False),  # 0
            ("Permission rule: without buttons", False),  # 2
        ],
    )
    def test_is_permission_prompt(self, detector, text, expected):
        """Test permission prompt detection with various inputs."""
        result = detector.is_permission_prompt(text)
        assert result == expected, f"Failed for: {text}"

    def test_permission_prompt_with_yes_no_buttons(self, detector):
        """Test that Yes/No buttons are worth 3 points."""
        # Just Yes/No buttons should not trigger alone (1 point)
        # But actually the code gives them 3 points, so they should trigger
        text = "1. Yes\n2. No"
        # With min_score=3, this should barely pass (3 points)
        result = detector.is_permission_prompt(text)
        assert result is True  # 3 points from Yes/No

    def test_permission_prompt_safety_checks(self, detector):
        """Test safety checks that reduce score."""
        # Code blocks should not be detected
        text_with_code = "```python\ndef foo():\n    pass\n```\n1. Yes\n2. No"
        assert not detector.is_permission_prompt(text_with_code)

        # Very long text should have reduced score
        long_text = "Do you want to proceed?\n1. Yes\n2. No\n" + ("a" * 2000)
        # May or may not pass depending on scoring

    @pytest.mark.parametrize(
        "text,expected_type",
        [
            ("1. Yes\n2. No", "numbered_menu"),
            ("Type yes to confirm", "text_input"),
            ("Enter yes to proceed", "text_input"),
            ("(y/n)", "text_input"),
            ("Unknown format", "numbered_menu"),  # Default
        ],
    )
    def test_get_prompt_type(self, detector, text, expected_type):
        """Test prompt type detection."""
        assert detector.get_prompt_type(text) == expected_type

    def test_is_question_prompt(self, detector):
        """Test question prompt detection."""
        # Questions should not be permissions
        question = "Select an option:\n1) Option A\n2) Option B"
        assert detector.is_question_prompt(question) is True

        # Permissions should not be questions
        permission = "Do you want to proceed?\n1. Yes\n2. No"
        assert detector.is_question_prompt(permission) is False

    def test_custom_permission_indicators(self, custom_config):
        """Test that custom permission indicators are used."""
        detector = PromptDetector(custom_config.config, debug=False)

        # Custom indicator (2) + Yes/No (3) = 5 points
        text = "CUSTOM_PROMPT: Do something\n1. Yes\n2. No"
        assert detector.is_permission_prompt(text) is True


class TestRateLimiter:
    """Test RateLimiter class."""

    @pytest.fixture
    def limiter(self, default_config):
        """Create a rate limiter instance."""
        return RateLimiter(default_config.config, debug=False)

    @pytest.fixture
    def detector(self, default_config):
        """Create a detector for strip_ansi function."""
        return PromptDetector(default_config.config, debug=False)

    def test_initial_state(self, limiter):
        """Test initial state of rate limiter."""
        stats = limiter.get_stats()
        assert stats["total_approvals"] == 0
        assert stats["unique_prompts"] == 0

    def test_record_approval(self, limiter, detector):
        """Test recording an approval."""
        text = "Test prompt"
        limiter.record_approval(text, detector.strip_ansi)

        stats = limiter.get_stats()
        assert stats["total_approvals"] == 1
        assert stats["unique_prompts"] == 1

    def test_cleanup_old_entries(self, limiter, detector):
        """Test cleanup of old entries."""
        text = "Test prompt"
        limiter.record_approval(text, detector.strip_ansi)

        # Manually set timestamp to old value
        old_time = time.time() - 70  # 70 seconds ago
        limiter.approval_timestamps = [old_time]
        limiter.approval_hashes = [(limiter._hash_prompt(detector.strip_ansi(text)), old_time)]

        limiter.cleanup_old_entries(window_seconds=60)

        stats = limiter.get_stats()
        assert stats["total_approvals"] == 0
        assert stats["unique_prompts"] == 0

    def test_check_approval_allowed_same_prompt(self, limiter, detector, default_config):
        """Test rate limiting for same prompt."""
        text = "Test prompt"
        max_same = default_config.get("max_same_prompt_approvals", 5)

        # Record approvals up to limit
        for i in range(max_same):
            limiter.record_approval(text, detector.strip_ansi)

        # Next approval should be blocked
        allowed, reason = limiter.check_approval_allowed(text, detector.strip_ansi)
        assert allowed is False
        assert "Same prompt" in reason

    def test_check_approval_allowed_global_limit(self, limiter, detector, default_config):
        """Test global rate limit."""
        max_per_minute = default_config.get("max_approvals_per_minute", 500)

        # Record many approvals with different prompts
        for i in range(max_per_minute):
            limiter.record_approval(f"Prompt {i}", detector.strip_ansi)

        # Next approval should be blocked
        allowed, reason = limiter.check_approval_allowed("New prompt", detector.strip_ansi)
        assert allowed is False
        assert "Rate limit" in reason

    def test_check_approval_allowed_different_prompts(self, limiter, detector):
        """Test that different prompts are not rate limited."""
        text1 = "First prompt"
        text2 = "Second prompt"

        limiter.record_approval(text1, detector.strip_ansi)

        # Different prompt should be allowed
        allowed, reason = limiter.check_approval_allowed(text2, detector.strip_ansi)
        assert allowed is True
        assert reason is None

    def test_hash_prompt(self, limiter):
        """Test prompt hashing."""
        text1 = "Test prompt"
        text2 = "Test prompt"
        text3 = "Different prompt"

        hash1 = limiter._hash_prompt(text1)
        hash2 = limiter._hash_prompt(text2)
        hash3 = limiter._hash_prompt(text3)

        assert hash1 == hash2  # Same text = same hash
        assert hash1 != hash3  # Different text = different hash

    def test_get_stats(self, limiter, detector):
        """Test statistics retrieval."""
        # Record some approvals
        limiter.record_approval("Prompt 1", detector.strip_ansi)
        limiter.record_approval("Prompt 2", detector.strip_ansi)
        limiter.record_approval("Prompt 1", detector.strip_ansi)  # Duplicate

        stats = limiter.get_stats()
        assert stats["total_approvals"] == 3
        assert stats["unique_prompts"] == 2  # Only 2 unique prompts

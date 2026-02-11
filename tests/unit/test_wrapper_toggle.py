"""
Unit tests for wrapper toggle behavior.

Tests that re-enabling auto-approve detects existing prompts.
"""

import pytest
from unittest.mock import Mock, MagicMock, patch
import os
import sys

from claude_autoapprove import Config
from claude_autoapprove.wrapper import ClaudeWrapper


class TestWrapperToggle:
    """Test wrapper toggle and buffering behavior."""

    @pytest.fixture
    def config(self):
        """Create a test configuration."""
        config = Config()
        config.config["auto_approve_delay"] = 1
        config.config["debug"] = False
        config.config["show_status_bar"] = False
        return config

    @pytest.fixture
    def wrapper(self, config):
        """Create a wrapper instance."""
        wrapper = ClaudeWrapper(config)
        # Mock the components that would normally be initialized
        wrapper.detector = Mock()
        wrapper.rate_limiter = Mock()
        wrapper.approval_manager = Mock()
        wrapper.status_bar = Mock()
        wrapper.master_fd = 1
        return wrapper

    def test_handle_claude_output_accumulates_when_disabled(self, wrapper):
        """Test that buffer accumulates even when auto-approve is disabled."""
        # Disable auto-approve
        wrapper.auto_approve_enabled = False

        # Mock detector to avoid actual detection
        wrapper.detector.is_permission_prompt = Mock(return_value=False)

        # Simulate output
        data = b"Some text from Claude\n1. Yes\n2. No\n"
        initial_buffer = ""

        with patch('os.write'):
            updated_buffer, detected = wrapper._handle_claude_output(data, initial_buffer)

        # Buffer should be updated even when disabled
        assert "Some text from Claude" in updated_buffer
        assert "1. Yes" in updated_buffer
        assert not detected  # Should not detect when disabled

    def test_handle_claude_output_detects_when_enabled(self, wrapper):
        """Test that detection happens when auto-approve is enabled."""
        # Enable auto-approve
        wrapper.auto_approve_enabled = True

        # Mock detector to detect permission prompt
        wrapper.detector.is_permission_prompt = Mock(return_value=True)
        wrapper.approval_manager.start_countdown = Mock(return_value=True)

        # Simulate output
        data = b"1. Yes\n2. No\n"
        initial_buffer = ""

        with patch('os.write'):
            updated_buffer, detected = wrapper._handle_claude_output(data, initial_buffer)

        # Detection should occur
        wrapper.detector.is_permission_prompt.assert_called_once()
        wrapper.approval_manager.start_countdown.assert_called_once()
        assert detected

    def test_toggle_checks_existing_buffer(self, wrapper):
        """Test that toggling on checks for existing prompts in buffer."""
        # Start with auto-approve disabled
        wrapper.auto_approve_enabled = False

        # Setup mocks
        wrapper.detector.is_permission_prompt = Mock(return_value=True)
        wrapper.approval_manager.start_countdown = Mock(return_value=True)
        wrapper.approval_manager.is_running = Mock(return_value=False)
        wrapper.status_bar.draw = Mock()
        wrapper.status_bar.clear = Mock()

        # Existing buffer with a prompt
        output_buffer = "Do you want to proceed?\n1. Yes\n2. No\n"

        # Simulate Ctrl+A toggle
        toggle_key = b"\x01"

        with patch('os.write'), patch('time.sleep'):
            should_continue, updated_buffer = wrapper._handle_user_input(toggle_key, output_buffer)

        # Should have toggled to enabled
        assert wrapper.auto_approve_enabled

        # Should have checked for existing prompt
        wrapper.detector.is_permission_prompt.assert_called_once_with(output_buffer)
        wrapper.approval_manager.start_countdown.assert_called_once()

        # Buffer should be cleared since prompt was detected
        assert updated_buffer == ""

    def test_toggle_off_does_not_check_buffer(self, wrapper):
        """Test that toggling off doesn't check buffer."""
        # Start with auto-approve enabled
        wrapper.auto_approve_enabled = True

        # Setup mocks
        wrapper.detector.is_permission_prompt = Mock(return_value=True)
        wrapper.approval_manager.start_countdown = Mock()
        wrapper.approval_manager.is_running = Mock(return_value=False)
        wrapper.status_bar.draw = Mock()
        wrapper.status_bar.clear = Mock()

        # Existing buffer with a prompt
        output_buffer = "Do you want to proceed?\n1. Yes\n2. No\n"

        # Simulate Ctrl+A toggle
        toggle_key = b"\x01"

        with patch('os.write'), patch('time.sleep'):
            should_continue, updated_buffer = wrapper._handle_user_input(toggle_key, output_buffer)

        # Should have toggled to disabled
        assert not wrapper.auto_approve_enabled

        # Should NOT have checked for existing prompt (not re-enabled)
        wrapper.detector.is_permission_prompt.assert_not_called()
        wrapper.approval_manager.start_countdown.assert_not_called()

        # Buffer should remain unchanged
        assert updated_buffer == output_buffer

    def test_toggle_with_empty_buffer(self, wrapper):
        """Test that toggling on with empty buffer doesn't cause issues."""
        # Start with auto-approve disabled
        wrapper.auto_approve_enabled = False

        # Setup mocks
        wrapper.detector.is_permission_prompt = Mock()
        wrapper.approval_manager.is_running = Mock(return_value=False)
        wrapper.status_bar.draw = Mock()
        wrapper.status_bar.clear = Mock()

        # Empty buffer
        output_buffer = ""

        # Simulate Ctrl+A toggle
        toggle_key = b"\x01"

        with patch('os.write'), patch('time.sleep'):
            should_continue, updated_buffer = wrapper._handle_user_input(toggle_key, output_buffer)

        # Should have toggled to enabled
        assert wrapper.auto_approve_enabled

        # Should NOT have checked empty buffer
        wrapper.detector.is_permission_prompt.assert_not_called()

    def test_toggle_with_non_prompt_buffer(self, wrapper):
        """Test that toggling on with non-prompt buffer doesn't start countdown."""
        # Start with auto-approve disabled
        wrapper.auto_approve_enabled = False

        # Setup mocks
        wrapper.detector.is_permission_prompt = Mock(return_value=False)
        wrapper.approval_manager.start_countdown = Mock()
        wrapper.approval_manager.is_running = Mock(return_value=False)
        wrapper.status_bar.draw = Mock()
        wrapper.status_bar.clear = Mock()

        # Buffer with regular text (not a prompt)
        output_buffer = "Here is some regular output from Claude"

        # Simulate Ctrl+A toggle
        toggle_key = b"\x01"

        with patch('os.write'), patch('time.sleep'):
            should_continue, updated_buffer = wrapper._handle_user_input(toggle_key, output_buffer)

        # Should have toggled to enabled
        assert wrapper.auto_approve_enabled

        # Should have checked but found no prompt
        wrapper.detector.is_permission_prompt.assert_called_once_with(output_buffer)
        wrapper.approval_manager.start_countdown.assert_not_called()

        # Buffer should remain unchanged
        assert updated_buffer == output_buffer

#!/usr/bin/env python3
"""
Test script for claude_wrapper.py
"""

import sys
import json
import hashlib
from pathlib import Path
from claude_wrapper import Config, ClaudeWrapper

def test_config_deep_merge():
    """Test that deep merge works correctly for nested configs"""
    print("Testing deep merge...")

    # Create a temporary config
    test_config_path = Path("/tmp/test_claude_wrapper.conf")

    # Write a config that overrides nested patterns
    test_config = {
        "auto_approve_delay": 2,
        "patterns": {
            "permission_indicators": [
                "Custom permission",
                "Allow this?"
            ]
        }
    }

    with open(test_config_path, 'w') as f:
        json.dump(test_config, f)

    # Load config
    config = Config(test_config_path)

    # Check that deep merge preserved both user and default values
    assert config.get("auto_approve_delay") == 2, "Failed to override auto_approve_delay"
    assert "Custom permission" in config.get("patterns", {}).get("permission_indicators", []), \
        "Failed to include custom permission indicator"
    assert "text_input_indicators" in config.get("patterns", {}), \
        "Failed to preserve default text_input_indicators"

    # Cleanup
    test_config_path.unlink()

    print("✓ Deep merge works correctly")
    return True

def test_counter_initialization():
    """Test that auto-approve counter is initialized"""
    print("Testing counter initialization...")

    config = Config()
    wrapper = ClaudeWrapper(config)

    assert hasattr(wrapper, '_auto_approve_count'), "Counter not initialized"
    assert wrapper._auto_approve_count == 0, "Counter not starting at 0"

    print("✓ Counter initialized correctly")
    return True

def test_ansi_stripping():
    """Test ANSI code stripping"""
    print("Testing ANSI code stripping...")

    config = Config()
    wrapper = ClaudeWrapper(config)

    # Test with cursor movement codes
    test_text = "\x1b[2A\x1b[3BHello\x1b[1;1H World\x1b[2J"
    clean = wrapper.strip_ansi(test_text)

    assert "Hello" in clean, "Failed to preserve text"
    assert "\x1b" not in clean, "Failed to remove ANSI codes"

    print("✓ ANSI stripping works")
    return True

def test_permission_detection():
    """Test permission prompt detection with scoring system"""
    print("Testing permission detection...")

    config = Config()
    wrapper = ClaudeWrapper(config)

    # Test cases - need score >= 3
    test_cases = [
        # Permission rule (2) + Yes/No (1) + Esc (1) = 4 ✓
        ("Permission rule: allow file access\n1. Yes\n2. No\nEsc to cancel", True),
        # Do you want (2) + Yes/No (1) = 3 ✓
        ("Do you want to proceed?\n1. Yes\n2. No", True),
        # Would you like (2) + Yes/No (1) = 3 ✓
        ("Would you like to proceed?\n1. Yes\n2. No", True),
        # Regular text = 0
        ("Just a regular message", False),
        ("This is a question about coding?", False),
        # Permission rule alone (2) = 2 (not enough)
        ("Permission rule: without buttons", False),
        # Just buttons (1) = 1 (not enough)
        ("1. Yes\n2. No", False),
    ]

    for text, expected in test_cases:
        result = wrapper.is_permission_prompt(text)
        assert result == expected, f"Failed for: {text} (expected {expected}, got {result})"

    print("✓ Permission detection works")
    return True

def test_custom_patterns():
    """Test that custom patterns from config are used"""
    print("Testing custom pattern usage...")

    # Create config with custom patterns
    test_config_path = Path("/tmp/test_claude_wrapper_patterns.conf")
    test_config = {
        "patterns": {
            "permission_indicators": [
                "CUSTOM_PROMPT"
            ]
        }
    }

    with open(test_config_path, 'w') as f:
        json.dump(test_config, f)

    config = Config(test_config_path)
    wrapper = ClaudeWrapper(config)

    # Test that custom pattern is detected
    # Custom indicator (2) + Yes/No (1) = 3 points ✓
    assert wrapper.is_permission_prompt("CUSTOM_PROMPT: Do something\n1. Yes\n2. No"), \
        "Failed to detect custom pattern"

    # Cleanup
    test_config_path.unlink()

    print("✓ Custom patterns work")
    return True

def test_prompt_type_detection():
    """Test prompt type detection"""
    print("Testing prompt type detection...")

    config = Config()
    wrapper = ClaudeWrapper(config)

    assert wrapper.get_prompt_type("1. Yes 2. No") == "numbered_menu"
    assert wrapper.get_prompt_type("Type yes to confirm") == "text_input"
    assert wrapper.get_prompt_type("Enter yes to proceed") == "text_input"

    print("✓ Prompt type detection works")
    return True

def test_strict_permission_detection():
    """Test that permission detection requires multiple indicators (scoring system)"""
    print("Testing strict permission detection...")

    config = Config()
    wrapper = ClaudeWrapper(config)

    # These should NOT trigger without enough indicators (score < 3)
    assert not wrapper.is_permission_prompt("Permission rule: something"), \
        "Should not detect without enough indicators (score too low)"
    assert not wrapper.is_permission_prompt("Do you want to proceed?"), \
        "Should not detect question alone"
    assert not wrapper.is_permission_prompt("1. Yes\n2. No"), \
        "Should not detect just options"

    # These SHOULD trigger with enough indicators (score >= 3)
    # Permission rule (2) + Yes/No (1) + Esc to cancel (1) = 4 points
    assert wrapper.is_permission_prompt("Permission rule: test\n1. Yes\n2. No\nEsc to cancel"), \
        "Should detect with multiple indicators"

    # Do you want to proceed (2) + Yes/No (1) + Tab to amend (1) = 4 points
    assert wrapper.is_permission_prompt("Do you want to proceed?\n1. Yes\n2. No\nTab to amend"), \
        "Should detect question with enough indicators"

    print("✓ Strict permission detection works")
    return True

def test_cooldown_mechanism():
    """Test cooldown prevents rapid re-triggers of SAME prompt only"""
    print("Testing cooldown mechanism...")
    import time

    config = Config()
    config.set("auto_approve_delay", 10)  # Long delay to prevent actual approval
    wrapper = ClaudeWrapper(config)

    # Simulate permission prompts (with enough indicators for score >= 3)
    prompt1 = "Permission rule: test\n1. Yes\n2. No\nEsc to cancel"
    prompt2 = "Permission rule: different test\n1. Yes\n2. No\nEsc to cancel"

    # First detection should work
    result1 = wrapper.handle_output(prompt1)
    assert result1 == True, "First detection should succeed"

    # Wait for countdown thread to start
    time.sleep(0.2)

    # Simulate that approval completed (set the hash and time without actually approving)
    wrapper._last_approval_time = time.time()
    wrapper._approved_prompt_hash = hashlib.md5(wrapper.strip_ansi(prompt1).encode()).hexdigest()

    # Stop the countdown thread
    wrapper.cancel_countdown()
    if wrapper._countdown_thread:
        wrapper._countdown_thread.join(timeout=1.0)

    # Restore the hash since cancel cleared it
    wrapper._approved_prompt_hash = hashlib.md5(wrapper.strip_ansi(prompt1).encode()).hexdigest()

    # Immediate re-detection of SAME prompt should be blocked
    result2 = wrapper.handle_output(prompt1)
    assert result2 == False, "Cooldown should block same prompt re-trigger"

    # But DIFFERENT prompt should be allowed immediately (even within cooldown)
    result3 = wrapper.handle_output(prompt2)
    if result3 != True:
        print(f"DEBUG: result3 = {result3}")
        print(f"DEBUG: _countdown_running = {wrapper._countdown_running}")
        print(f"DEBUG: time_since_last = {time.time() - wrapper._last_approval_time}")
        print(f"DEBUG: _approved_prompt_hash = {wrapper._approved_prompt_hash}")
        print(f"DEBUG: prompt2_hash = {hashlib.md5(wrapper.strip_ansi(prompt2).encode()).hexdigest()}")
    assert result3 == True, "Different prompt should be approved immediately"

    # Cleanup
    wrapper.cancel_countdown()
    if wrapper._countdown_thread:
        wrapper._countdown_thread.join(timeout=1.0)

    print("✓ Cooldown mechanism works")
    return True

def test_duplicate_detection():
    """Test duplicate prompt detection"""
    print("Testing duplicate detection...")
    import time

    config = Config()
    config.set("cooldown_seconds", 1.0)  # Shorter for testing
    config.set("auto_approve_delay", 10)  # Long delay so we can test without auto-approval
    wrapper = ClaudeWrapper(config)

    # Use prompt with enough indicators (score >= 3)
    prompt = "Permission rule: test\n1. Yes\n2. No\nEsc to cancel"

    # First detection
    result1 = wrapper.handle_output(prompt)
    assert result1 == True, "First detection should succeed"

    # Simulate approval completing (without waiting for actual countdown)
    time.sleep(0.1)
    wrapper._last_approval_time = time.time()

    # Cancel the countdown thread
    wrapper.cancel_countdown()
    if wrapper._countdown_thread:
        wrapper._countdown_thread.join(timeout=1.0)

    # Note: cancel_countdown() clears the hash, which is intentional behavior
    # So we need to set it again to test duplicate detection
    wrapper._approved_prompt_hash = hashlib.md5(wrapper.strip_ansi(prompt).encode()).hexdigest()

    # Same prompt within cooldown should be blocked
    result2 = wrapper.handle_output(prompt)
    assert result2 == False, "Cooldown should block immediate re-trigger"

    # Wait for cooldown to pass but within hash expiry (3x cooldown)
    time.sleep(1.5)

    # Same prompt after cooldown but within hash expiry should be blocked
    result3 = wrapper.handle_output(prompt)
    assert result3 == False, "Duplicate detection should block same prompt within expiry"

    # Wait for hash to expire (need to wait 3x cooldown total = 3 seconds)
    time.sleep(2.0)

    # After hash expiry, same prompt should work again
    result4 = wrapper.handle_output(prompt)
    assert result4 == True, "After hash expiry, same prompt should be detected"

    # Cleanup
    wrapper.cancel_countdown()

    print("✓ Duplicate detection works")
    return True

def main():
    """Run all tests"""
    print("=" * 60)
    print("Claude Wrapper Test Suite")
    print("=" * 60)

    tests = [
        test_config_deep_merge,
        test_counter_initialization,
        test_ansi_stripping,
        test_permission_detection,
        test_custom_patterns,
        test_prompt_type_detection,
        test_strict_permission_detection,
        test_cooldown_mechanism,
        test_duplicate_detection,
    ]

    failed = []
    for test in tests:
        try:
            test()
        except Exception as e:
            print(f"✗ {test.__name__} failed: {e}")
            failed.append(test.__name__)
            import traceback
            traceback.print_exc()

    print("=" * 60)
    if failed:
        print(f"FAILED: {len(failed)} test(s) failed:")
        for name in failed:
            print(f"  - {name}")
        return 1
    else:
        print("SUCCESS: All tests passed!")
        return 0

if __name__ == '__main__':
    sys.exit(main())

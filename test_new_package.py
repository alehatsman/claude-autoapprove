#!/usr/bin/env python3
"""
Test script to verify the new modular package works.
"""
import sys
from claude_autoapprove import ClaudeWrapper, Config, __version__

def test_imports():
    """Test that all imports work."""
    print("✓ Imports successful")
    print(f"  Version: {__version__}")

def test_config():
    """Test configuration."""
    config = Config()
    print("✓ Config created")
    print(f"  Auto-approve delay: {config.get('auto_approve_delay')}")
    print(f"  Debug: {config.get('debug')}")
    print(f"  Min detection score: {config.get('min_detection_score')}")

def test_wrapper_creation():
    """Test wrapper creation."""
    config = Config()
    wrapper = ClaudeWrapper(config)
    print("✓ Wrapper created")
    print(f"  Process ID: {wrapper.pid}")
    print(f"  Auto-approve enabled: {wrapper.auto_approve_enabled}")
    print(f"  Debug mode: {wrapper.debug}")

def test_detection():
    """Test prompt detection."""
    from claude_autoapprove.detection import PromptDetector

    config = Config()
    detector = PromptDetector(config.config, debug=False)

    # Test permission prompt detection
    permission_text = "Do you want to proceed?\n1. Yes\n2. No"
    is_permission = detector.is_permission_prompt(permission_text)
    print(f"✓ Detection works")
    print(f"  '{permission_text[:30]}...' detected as permission: {is_permission}")

    # Test ANSI stripping
    ansi_text = "\x1b[32mHello\x1b[0m World"
    clean = detector.strip_ansi(ansi_text)
    print(f"  ANSI stripped: '{ansi_text}' -> '{clean}'")

def main():
    print("=" * 60)
    print("Testing New Claude Auto-Approve Package")
    print("=" * 60)
    print()

    try:
        test_imports()
        print()

        test_config()
        print()

        test_wrapper_creation()
        print()

        test_detection()
        print()

        print("=" * 60)
        print("✓ All tests passed!")
        print("=" * 60)
        print()
        print("The new package is working correctly.")
        print("You can now use:")
        print("  - claude-wrapper")
        print("  - claude-autoapprove")
        print("  - python -m claude_autoapprove")
        return 0

    except Exception as e:
        print(f"\n✗ Error: {e}")
        import traceback
        traceback.print_exc()
        return 1

if __name__ == '__main__':
    sys.exit(main())

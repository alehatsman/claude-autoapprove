"""
Command-line interface for Claude Auto-Approve.
"""

import argparse
import sys
from pathlib import Path

from .config import Config
from .constants import DEFAULT_AUTO_APPROVE_DELAY, DEFAULT_CONFIG_PATH, DEFAULT_LOG_DIR, __version__
from .exceptions import ClaudeNotFoundError, TerminalSetupError
from .wrapper import ClaudeWrapper


def create_parser() -> argparse.ArgumentParser:
    """
    Create and configure argument parser.

    Returns:
        Configured ArgumentParser instance
    """
    parser = argparse.ArgumentParser(
        prog="claude-wrapper",
        description="Production-ready wrapper for Claude Code with auto-approve functionality",
        epilog=f"Version {__version__} - Config file: {DEFAULT_CONFIG_PATH}",
        formatter_class=argparse.RawDescriptionHelpFormatter,
    )

    parser.add_argument("--version", action="version", version=f"%(prog)s {__version__}")

    parser.add_argument(
        "--delay",
        type=int,
        metavar="SECONDS",
        help=f"Seconds to wait before auto-approving (default: from config or {DEFAULT_AUTO_APPROVE_DELAY})",
    )

    parser.add_argument(
        "--no-auto-approve",
        action="store_true",
        help="Disable auto-approve (just wrap Claude without auto-approval)",
    )

    parser.add_argument(
        "--no-status-bar", action="store_true", help="Disable status bar display (cleaner output)"
    )

    parser.add_argument(
        "--debug",
        action="store_true",
        help=f"Enable debug logging (logs saved to: {DEFAULT_LOG_DIR}/wrapper_<PID>.log)",
    )

    parser.add_argument(
        "--config",
        type=Path,
        metavar="PATH",
        help=f"Path to config file (default: {DEFAULT_CONFIG_PATH})",
    )

    parser.add_argument(
        "--init-config", action="store_true", help="Create default configuration file and exit"
    )

    parser.add_argument(
        "--claude-path", metavar="PATH", help="Path to Claude Code executable (default: claude)"
    )

    parser.add_argument("claude_args", nargs="*", help="Arguments to pass to Claude Code")

    return parser


def handle_init_config(config: Config) -> int:
    """
    Handle --init-config command.

    Args:
        config: Config instance

    Returns:
        Exit code (0 for success)
    """
    config.save_config()
    print(f"Configuration file created at: {config.config_path}")
    print("Edit this file to customize behavior")
    return 0


def apply_cli_overrides(config: Config, args: argparse.Namespace) -> None:
    """
    Apply command-line argument overrides to configuration.

    Args:
        config: Config instance to modify
        args: Parsed command-line arguments
    """
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


def main() -> int:
    """
    Main entry point for CLI.

    Returns:
        Exit code from Claude process or error code

    Example:
        >>> sys.exit(main())
    """
    # Parse arguments
    parser = create_parser()
    args = parser.parse_args()

    # Load configuration
    config = Config(args.config)

    # Handle --init-config
    if args.init_config:
        return handle_init_config(config)

    # Apply CLI overrides
    apply_cli_overrides(config, args)

    # Create and run wrapper
    wrapper = ClaudeWrapper(config)

    try:
        exit_code = wrapper.run(args.claude_args)
        return exit_code if exit_code is not None else 0

    except KeyboardInterrupt:
        sys.stderr.write("\n\n\033[33mInterrupted by user\033[0m\n")
        return 130

    except ClaudeNotFoundError as e:
        sys.stderr.write(f"\n\033[31mError: {e}\033[0m\n")
        return 127

    except TerminalSetupError as e:
        sys.stderr.write(f"\n\033[31mTerminal Error: {e}\033[0m\n")
        return 1

    except Exception as e:
        sys.stderr.write(f"\n\033[31mUnexpected Error: {e}\033[0m\n")
        if config.get("debug"):
            import traceback

            traceback.print_exc()
        return 1


if __name__ == "__main__":
    sys.exit(main())

"""
Configuration management for Claude Auto-Approve.
"""

import copy
import json
import logging
from pathlib import Path
from typing import Any, Dict, Optional

from .constants import DEFAULT_AUTO_APPROVE_DELAY, DEFAULT_CONFIG_PATH, DEFAULT_LOG_DIR
from .exceptions import ConfigurationError


class Config:
    """
    Configuration management for Claude Wrapper.

    Handles loading, saving, and accessing configuration values with
    support for nested dictionaries and deep merging.

    Attributes:
        config_path: Path to the configuration file
        config: Dictionary containing all configuration values

    Example:
        >>> config = Config()
        >>> config.get("auto_approve_delay")
        1
        >>> config.set("debug", True)
        >>> config.save_config()
    """

    DEFAULT_CONFIG = {
        "auto_approve_delay": DEFAULT_AUTO_APPROVE_DELAY,
        "debug": False,
        "log_dir": str(DEFAULT_LOG_DIR),
        "log_retention_days": 7,
        "claude_path": "claude",
        "auto_approve_enabled": True,
        "show_status_bar": True,
        "toggle_key": "\x01",  # Ctrl+A
        "min_detection_score": 3,
        "max_approvals_per_minute": 500,
        "max_same_prompt_approvals": 5,
        "idle_detection_enabled": True,
        "idle_timeout_seconds": 2.5,
        "patterns": {
            "permission_indicators": [],
            "text_input_indicators": [r"Type.*yes", r"Enter.*yes", r"\(y/n\)"],
        },
    }

    def __init__(self, config_path: Optional[Path] = None):
        """
        Initialize configuration.

        Args:
            config_path: Optional path to configuration file.
                        Defaults to ~/.claude_wrapper.conf
        """
        self.config_path = config_path or DEFAULT_CONFIG_PATH
        self.config = self._load_config()

    def _deep_merge(self, base: Dict[str, Any], override: Dict[str, Any]) -> Dict[str, Any]:
        """
        Recursively merge override into base, preserving nested structures.

        Args:
            base: Base dictionary
            override: Dictionary with values to override

        Returns:
            Merged dictionary

        Example:
            >>> config._deep_merge(
            ...     {"a": {"b": 1, "c": 2}},
            ...     {"a": {"b": 3, "d": 4}}
            ... )
            {"a": {"b": 3, "c": 2, "d": 4}}
        """
        result = copy.deepcopy(base)
        for key, value in override.items():
            if isinstance(value, dict) and key in result and isinstance(result[key], dict):
                result[key] = self._deep_merge(result[key], value)
            else:
                result[key] = value
        return result

    def _load_config(self) -> Dict[str, Any]:
        """
        Load configuration from file or use defaults.

        Returns:
            Configuration dictionary

        Raises:
            ConfigurationError: If config file is malformed
        """
        if self.config_path.exists():
            try:
                with open(self.config_path, "r") as f:
                    user_config = json.load(f)
                    if not isinstance(user_config, dict):
                        raise ConfigurationError(
                            f"Config file must contain a JSON object, got {type(user_config)}"
                        )
                    # Use deep merge to handle nested dictionaries properly
                    config = self._deep_merge(self.DEFAULT_CONFIG, user_config)
                    return config
            except json.JSONDecodeError as e:
                logging.warning(f"Failed to parse config from {self.config_path}: {e}")
                return copy.deepcopy(self.DEFAULT_CONFIG)
            except Exception as e:
                logging.warning(f"Failed to load config from {self.config_path}: {e}")
                return copy.deepcopy(self.DEFAULT_CONFIG)
        return copy.deepcopy(self.DEFAULT_CONFIG)

    def save_config(self) -> None:
        """
        Save current configuration to file.

        Raises:
            ConfigurationError: If unable to save configuration
        """
        try:
            # Ensure parent directory exists
            self.config_path.parent.mkdir(parents=True, exist_ok=True)

            with open(self.config_path, "w") as f:
                json.dump(self.config, f, indent=2)
        except Exception as e:
            raise ConfigurationError(f"Failed to save config to {self.config_path}: {e}")

    def get(self, key: str, default: Any = None) -> Any:
        """
        Get configuration value.

        Args:
            key: Configuration key
            default: Default value if key not found

        Returns:
            Configuration value or default

        Example:
            >>> config.get("auto_approve_delay", 5)
            1
        """
        return self.config.get(key, default)

    def set(self, key: str, value: Any) -> None:
        """
        Set configuration value.

        Args:
            key: Configuration key
            value: Value to set

        Example:
            >>> config.set("debug", True)
        """
        self.config[key] = value

    def validate(self) -> None:
        """
        Validate configuration values.

        Raises:
            ConfigurationError: If configuration is invalid
        """
        # Validate numeric values
        if self.get("auto_approve_delay", 0) < 0:
            raise ConfigurationError("auto_approve_delay must be non-negative")

        if self.get("min_detection_score", 0) < 0:
            raise ConfigurationError("min_detection_score must be non-negative")

        if self.get("max_approvals_per_minute", 0) <= 0:
            raise ConfigurationError("max_approvals_per_minute must be positive")

        if self.get("max_same_prompt_approvals", 0) <= 0:
            raise ConfigurationError("max_same_prompt_approvals must be positive")

        if self.get("idle_timeout_seconds", 0) <= 0:
            raise ConfigurationError("idle_timeout_seconds must be positive")

        if self.get("log_retention_days", 0) <= 0:
            raise ConfigurationError("log_retention_days must be positive")

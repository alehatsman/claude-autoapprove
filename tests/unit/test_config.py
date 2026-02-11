"""
Unit tests for configuration management.
"""

import json
from pathlib import Path

import pytest

from claude_autoapprove.config import Config
from claude_autoapprove.exceptions import ConfigurationError


class TestConfig:
    """Test Config class."""

    def test_default_config_values(self, default_config):
        """Test that default values are set correctly."""
        assert default_config.get("auto_approve_delay") == 1
        assert default_config.get("debug") is False
        assert default_config.get("auto_approve_enabled") is True
        assert default_config.get("show_status_bar") is True
        assert default_config.get("min_detection_score") == 3

    def test_load_custom_config(self, temp_config_file):
        """Test loading custom configuration from file."""
        custom_settings = {"auto_approve_delay": 5, "debug": True}

        with open(temp_config_file, "w") as f:
            json.dump(custom_settings, f)

        config = Config(temp_config_file)
        assert config.get("auto_approve_delay") == 5
        assert config.get("debug") is True

    def test_deep_merge(self, temp_config_file):
        """Test that deep merge preserves nested structures."""
        custom_settings = {
            "auto_approve_delay": 2,
            "patterns": {"permission_indicators": ["Custom permission", "Allow this?"]},
        }

        with open(temp_config_file, "w") as f:
            json.dump(custom_settings, f)

        config = Config(temp_config_file)

        # Check that custom values are set
        assert config.get("auto_approve_delay") == 2
        patterns = config.get("patterns", {})
        assert "Custom permission" in patterns.get("permission_indicators", [])

        # Check that default nested values are preserved
        assert "text_input_indicators" in patterns
        assert len(patterns.get("text_input_indicators", [])) > 0

    def test_save_config(self, temp_config_file):
        """Test saving configuration to file."""
        config = Config(temp_config_file)
        config.set("auto_approve_delay", 10)
        config.save_config()

        # Reload and verify
        new_config = Config(temp_config_file)
        assert new_config.get("auto_approve_delay") == 10

    def test_get_with_default(self, default_config):
        """Test getting non-existent key with default value."""
        value = default_config.get("nonexistent_key", "default_value")
        assert value == "default_value"

    def test_set_value(self, default_config):
        """Test setting configuration value."""
        default_config.set("custom_key", "custom_value")
        assert default_config.get("custom_key") == "custom_value"

    def test_validate_positive_values(self, default_config):
        """Test validation of positive numeric values."""
        # Valid values should pass
        default_config.validate()

        # Invalid values should raise error
        default_config.set("auto_approve_delay", -1)
        with pytest.raises(ConfigurationError):
            default_config.validate()

    def test_validate_min_detection_score(self, default_config):
        """Test validation of min_detection_score."""
        default_config.set("min_detection_score", -1)
        with pytest.raises(ConfigurationError):
            default_config.validate()

    def test_validate_rate_limits(self, default_config):
        """Test validation of rate limit values."""
        default_config.set("max_approvals_per_minute", 0)
        with pytest.raises(ConfigurationError):
            default_config.validate()

        default_config.set("max_approvals_per_minute", 500)
        default_config.set("max_same_prompt_approvals", -1)
        with pytest.raises(ConfigurationError):
            default_config.validate()

    def test_invalid_json_falls_back_to_defaults(self, temp_config_file):
        """Test that invalid JSON falls back to defaults."""
        with open(temp_config_file, "w") as f:
            f.write("{invalid json")

        config = Config(temp_config_file)
        # Should have default values
        assert config.get("auto_approve_delay") == 1

    def test_non_dict_config_raises_error(self, temp_config_file):
        """Test that non-dictionary config raises error."""
        with open(temp_config_file, "w") as f:
            json.dump(["not", "a", "dict"], f)

        # Should fall back to defaults with warning
        config = Config(temp_config_file)
        assert config.get("auto_approve_delay") == 1

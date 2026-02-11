"""
Shared test fixtures and configuration for pytest.
"""

import json
import tempfile
from pathlib import Path

import pytest

from claude_autoapprove.config import Config


@pytest.fixture
def temp_config_file():
    """Create a temporary config file for testing."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".conf", delete=False) as f:
        config_path = Path(f.name)
        yield config_path
    # Cleanup
    if config_path.exists():
        config_path.unlink()


@pytest.fixture
def default_config():
    """Create a config instance with default settings."""
    with tempfile.NamedTemporaryFile(mode="w", suffix=".conf", delete=False) as f:
        config_path = Path(f.name)

    config = Config(config_path)
    yield config

    # Cleanup
    if config_path.exists():
        config_path.unlink()


@pytest.fixture
def custom_config(temp_config_file):
    """Create a config instance with custom settings."""
    custom_settings = {
        "auto_approve_delay": 2,
        "debug": True,
        "patterns": {"permission_indicators": ["CUSTOM_PROMPT"]},
    }

    with open(temp_config_file, "w") as f:
        json.dump(custom_settings, f)

    config = Config(temp_config_file)
    yield config


@pytest.fixture
def sample_permission_prompt():
    """Sample permission prompt text."""
    return "Permission rule: allow file access\n1. Yes\n2. No\nEsc to cancel"


@pytest.fixture
def sample_question_prompt():
    """Sample question prompt text."""
    return "Select an option:\n1) Option A\n2) Option B\n3) Option C"


@pytest.fixture
def sample_prompts():
    """Dictionary of sample prompts for testing."""
    return {
        "permission_with_rule": "Permission rule: allow file access\n1. Yes\n2. No\nEsc to cancel",
        "permission_do_you_want": "Do you want to proceed?\n1. Yes\n2. No",
        "permission_would_you_like": "Would you like to proceed?\n1. Yes\n2. No",
        "permission_create_file": "Do you want to create this file?\n1. Yes\n2. No",
        "question": "Select an option:\n1) Option A\n2) Option B",
        "regular_text": "This is just regular text output",
        "yes_no_only": "1. Yes\n2. No",
        "permission_rule_only": "Permission rule: something",
    }

"""
Enable running as: python -m claude_autoapprove
"""

import sys

from .cli import main

if __name__ == "__main__":
    sys.exit(main())

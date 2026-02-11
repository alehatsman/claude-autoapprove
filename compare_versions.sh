#!/bin/bash
# Compare old vs new implementation

echo "============================================================"
echo "Comparing Old vs New Implementation"
echo "============================================================"
echo ""

echo "OLD VERSION (claude_wrapper.py):"
echo "  - Single file: $(wc -l < claude_wrapper.py) lines"
echo "  - Run with: ./claude_wrapper.py"
echo ""

echo "NEW VERSION (src/claude_autoapprove/):"
total_lines=$(find src/claude_autoapprove -name "*.py" -exec wc -l {} + | tail -1 | awk '{print $1}')
file_count=$(find src/claude_autoapprove -name "*.py" -type f | wc -l)
echo "  - Modular package: $file_count files, $total_lines lines"
echo "  - Run with: claude-wrapper"
echo ""

echo "============================================================"
echo "Testing Both Versions"
echo "============================================================"
echo ""

echo "1. OLD VERSION - Check if it exists:"
if [ -f "./claude_wrapper.py" ]; then
    echo "   ✓ claude_wrapper.py exists"
    echo "   - Can run: ./claude_wrapper.py --version"
else
    echo "   ✗ claude_wrapper.py not found"
fi
echo ""

echo "2. NEW VERSION - Check if installed:"
if command -v claude-wrapper &> /dev/null; then
    echo "   ✓ claude-wrapper command available"
    echo "   - Version: $(claude-wrapper --version 2>&1 | head -1)"
    echo "   - Can run: claude-wrapper --version"
else
    echo "   ✗ claude-wrapper not installed"
    echo "   - Run: pip install -e ."
fi
echo ""

echo "============================================================"
echo "Feature Comparison"
echo "============================================================"
echo ""

cat <<EOF
OLD VERSION (claude_wrapper.py):
  ✓ Auto-approve with countdown
  ✓ Permission detection
  ✓ Rate limiting
  ✓ Status bar
  ✗ No package structure
  ✗ No tests
  ✗ No type hints
  ✗ No CI/CD
  ✗ Not pip-installable

NEW VERSION (claude-autoapprove):
  ✓ Auto-approve with countdown
  ✓ Permission detection
  ✓ Rate limiting
  ✓ Status bar
  ✓ Proper package structure
  ✓ 48 unit tests
  ✓ Full type hints
  ✓ CI/CD pipeline
  ✓ pip-installable
  ✓ Production-ready
EOF

echo ""
echo "============================================================"
echo "Recommendation"
echo "============================================================"
echo ""
echo "Use the NEW VERSION (claude-wrapper) for:"
echo "  - Production use"
echo "  - Development and testing"
echo "  - Distribution to others"
echo ""
echo "The old claude_wrapper.py can be kept as reference or removed."
echo ""

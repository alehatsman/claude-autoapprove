#!/bin/bash
# Test script for autoapprove wrapper

set -e

echo "=== Auto-Approve Wrapper Tests ==="
echo ""

# Build if needed
if [ ! -f "./autoapprove" ]; then
    echo "Building autoapprove..."
    go build -o autoapprove autoapprove.go
    echo ""
fi

echo "Test 1: Basic prompt detection"
echo "Expected: Automatically answer 'y' to the prompt"
echo "---"
./autoapprove -- bash -c 'echo "Do you want to continue? [y/n]"; sleep 0.5; read -t 2 answer; echo "Received: $answer"' 2>/dev/null
echo ""
echo ""

echo "Test 2: Idle timeout (short)"
echo "Expected: Nudge after 3 seconds of silence"
echo "---"
timeout 10 ./autoapprove --idle 3s -- bash -c 'echo "Starting task..."; sleep 5; echo "Done"' 2>&1 | grep -E "(IDLE_DETECTED|Starting|Done)" || true
echo ""
echo ""

echo "Test 3: Dangerous command detection"
echo "Expected: Switch to manual mode, no auto-approval"
echo "---"
timeout 5 ./autoapprove -- bash -c 'echo "Next command: rm -rf /"; echo "Continue? [y/n]"; sleep 10' 2>&1 | grep -E "(DANGER_DETECTED|manual mode)" || true
echo ""
echo ""

echo "Test 4: Show default configuration"
echo "---"
./autoapprove --show-defaults | head -20
echo ""

echo "=== Tests Complete ==="

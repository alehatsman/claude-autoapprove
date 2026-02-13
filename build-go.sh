#!/bin/bash
# Build script for claude-autoapprove

set -e

echo "Building claude-autoapprove..."

# Download dependencies
go mod download

# Build
go build -o claude-autoapprove main.go

echo "✓ Built successfully: ./claude-autoapprove"
echo ""
echo "Usage:"
echo "  ./claude-autoapprove [claude args...]"
echo ""
echo "Examples:"
echo "  ./claude-autoapprove"
echo "  ./claude-autoapprove --help"
echo "  ./claude-autoapprove 'review this code'"

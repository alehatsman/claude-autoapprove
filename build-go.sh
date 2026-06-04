#!/bin/bash
# Build script for cry-aye

set -e

echo "Building cry-aye..."

# Download dependencies
go mod download

# Build
go build -o cry-aye main.go

echo "✓ Built successfully: ./cry-aye"
echo ""
echo "Usage:"
echo "  ./cry-aye [claude args...]"
echo ""
echo "Examples:"
echo "  ./cry-aye"
echo "  ./cry-aye --help"
echo "  ./cry-aye 'review this code'"

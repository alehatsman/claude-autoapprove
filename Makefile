.PHONY: build install clean test deps show-config

# Binary name
BINARY=autoapprove

# Build the binary
build:
	go build -o $(BINARY) autoapprove.go

# Install to GOPATH/bin
install:
	go install autoapprove.go

# Clean build artifacts
clean:
	rm -f $(BINARY)
	rm -f autoapprove.log
	rm -f session.log

# Download dependencies
deps:
	go mod download
	go mod tidy

# Show default configuration
show-config: build
	./$(BINARY) --show-defaults

# Test with a simple interactive command
test: build
	@echo "Testing with simple prompt..."
	./$(BINARY) -- bash -c 'echo "Do you want to continue? [y/n]"; sleep 1; echo "Done"' 2> test.log
	@echo "\nLog output:"
	@cat test.log
	@rm test.log

# Run with Claude (edit command as needed)
run-claude: build
	./$(BINARY) --idle 15s --config rules.yaml -- claude

# Help
help:
	@echo "Available targets:"
	@echo "  build        - Build the autoapprove binary"
	@echo "  install      - Install to GOPATH/bin"
	@echo "  clean        - Remove build artifacts"
	@echo "  deps         - Download and tidy dependencies"
	@echo "  show-config  - Display default configuration"
	@echo "  test         - Run a simple test"
	@echo "  run-claude   - Run with Claude CLI (example)"
	@echo "  help         - Show this help message"

.PHONY: build install clean

BINARY_NAME=claude-autoapprove
INSTALL_PATH=/usr/local/bin

build:
	@echo "Building $(BINARY_NAME)..."
	@go mod download
	@go build -o $(BINARY_NAME) ./cmd/claude-autoapprove
	@echo "✓ Built successfully: ./$(BINARY_NAME)"

install: build
	@echo "Installing $(BINARY_NAME) to $(INSTALL_PATH)..."
	@chmod +x $(BINARY_NAME)
	@sudo cp $(BINARY_NAME) $(INSTALL_PATH)/$(BINARY_NAME)
	# @sudo chmod +x $(INSTALL_PATH)/$(BINARY_NAME)
	@echo "✓ Installed successfully: $(INSTALL_PATH)/$(BINARY_NAME)"

clean:
	@echo "Cleaning up..."
	@rm -f $(BINARY_NAME)
	@echo "✓ Cleaned"

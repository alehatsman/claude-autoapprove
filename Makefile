.PHONY: build install clean format

BINARY_NAME=cry-aye
INSTALL_PATH=/usr/local/bin

build:
	@echo "Building $(BINARY_NAME)..."
	@go mod download
	@go build -o $(BINARY_NAME) ./cmd/cry-aye
	@echo "✓ Built successfully: ./$(BINARY_NAME)"

install: build
	@echo "Installing $(BINARY_NAME) to $(INSTALL_PATH)..."
	@chmod +x $(BINARY_NAME)
	@sudo cp $(BINARY_NAME) $(INSTALL_PATH)/$(BINARY_NAME)
	@echo "✓ Installed successfully: $(INSTALL_PATH)/$(BINARY_NAME)"

format:
	@gofmt -w .

clean:
	@echo "Cleaning up..."
	@rm -f $(BINARY_NAME)
	@echo "✓ Cleaned"

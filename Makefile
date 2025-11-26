.PHONY: build test clean

# Default target - build for current platform
all:
	@echo "Building surf for current platform..."
	@go build -o surf .
	@echo "Build complete: ./surf"

# Build all platform binaries
build:
	@echo "Building surf for all platforms..."
	@rm -f surf surf-darwin-arm64 surf-darwin-amd64 surf-linux-amd64
	@echo "Downloading Go dependencies..."
	@go mod download
	@echo "Building for macOS ARM64..."
	@GOOS=darwin GOARCH=arm64 go build -o surf-darwin-arm64 .
	@echo "Building for macOS Intel..."
	@GOOS=darwin GOARCH=amd64 go build -o surf-darwin-amd64 .
	@echo "Building for Linux x86_64..."
	@GOOS=linux GOARCH=amd64 go build -o surf-linux-amd64 .
	@echo "Creating local symlink..."
	@ln -sf surf-$(shell uname -s | tr '[:upper:]' '[:lower:]')-$(shell uname -m | sed 's/x86_64/amd64/') surf
	@echo "Build complete!"
	@echo "Binaries:"
	@echo "  surf-darwin-arm64 ($(shell du -h surf-darwin-arm64 2>/dev/null | cut -f1 || echo 'N/A')) - macOS Apple Silicon"
	@echo "  surf-darwin-amd64 ($(shell du -h surf-darwin-amd64 2>/dev/null | cut -f1 || echo 'N/A')) - macOS Intel"
	@echo "  surf-linux-amd64  ($(shell du -h surf-linux-amd64 2>/dev/null | cut -f1 || echo 'N/A')) - Linux x86_64"
	@echo "  surf -> $(shell readlink surf 2>/dev/null || echo 'local binary')"

# Run tests
test: build
	@echo "Running comprehensive test suite..."
	@go test -v -timeout=300s

# Clean build artifacts
clean:
	@echo "Cleaning build artifacts..."
	@rm -f surf surf-darwin-arm64 surf-darwin-amd64 surf-linux-amd64
	@rm -f test-screenshot-*.png
	@rm -rf ~/.surf/profiles/test-*
	@echo "Clean complete"

build:
	@echo "Building fcards..."
	@mkdir -p bin
	CGO_ENABLED=1 go build -tags "fts5" -o bin/fcards main.go
	@echo "Build complete! Binary at: bin/fcards"

install:
	@echo "Installing fcards to $(shell go env GOPATH)/bin..."
	CGO_ENABLED=1 go build -tags "fts5" -o "$(shell go env GOPATH)/bin/fcards" main.go
	@echo "Installation complete!"
	@echo ""
	@echo "Make sure $(shell go env GOPATH)/bin is in your PATH"
	@echo "You can now run: fcards"

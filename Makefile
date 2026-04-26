.DEFAULT_GOAL := help

.PHONY: help fmt test generate

help:
	@echo "Available targets:"
	@echo "  fmt       Format all Go files"
	@echo "  test      Run Go unit tests"
	@echo "  generate  Generate proto code"

fmt:
	@files=$$(find . -type f -name '*.go' -not -path './.git/*'); \
	if [ -z "$$files" ]; then \
		echo "no Go files to format"; \
	else \
		gofmt -w $$files; \
	fi

test:
	@test -f go.mod || (echo "go.mod not found. initialize the Go module first."; exit 1)
	go test ./...

generate:
	@test -f proto/generate.sh || (echo "proto/generate.sh not found"; exit 1)
	@echo "generating proto code..."
	@bash proto/generate.sh

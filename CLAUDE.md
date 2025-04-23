# CLAUDE.md

This file provides guidance to Claude Code (claude.ai/code) when working with code in this repository.

## Build and Testing
- Run tests: `docker compose -f docker-compose-test.yml up -d && go test ./...`
- Run single test: `go test -v ./path/to/package -run TestName`
- Format code: `go fmt ./...`
- Lint: `golangci-lint run`

## Code Style Guidelines
- Use standard Go formatting with `go fmt`
- Error handling: Return errors rather than using panics
- Imports: Grouped by standard lib, external packages, internal packages
- Naming: Use camelCase for private, PascalCase for exported items
- Context: Pass context.Context as first parameter to functions
- Testing: Use table-driven tests with testify/require for assertions
- Logging: Use the logger package with appropriate log levels
- Tracing: Include tracing in context-based operations
- Documentation: Document all exported functions, types, and constants
- Dependencies: Prefer composition over inheritance
# Changelog

All notable changes to this project will be documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.0.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Added
- Structured logging system (pkg/logger) based on zap
- Sentinel errors package (pkg/errors) with 42 error definitions
- Shared API client package (pkg/client)
- API pagination support (limit/offset)
- Input validation for task creation
- Request body size limit middleware (10MB)
- Version command for all CLI tools
- Makefile with multi-platform build support
- Dockerfile for containerized deployment
- Configuration file support (YAML)
- Quick start guide (docs/QUICKSTART.md)
- API reference documentation
- Architecture documentation

### Changed
- Replaced 28 fmt.Printf with structured logging in core packages
- Replaced 46 domain-specific errors with sentinel errors
- Merged service package into server
- Merged ai package into executor
- Merged debugger package into skill
- Extracted shared API client from CLI tools
- Updated API to use request context instead of context.Background()
- Updated README to reflect final project state

### Removed
- Dashboard package (retired, replaced by api)
- Generator package (unused)
- Research package (unused)
- UI package (unused)
- Config package (unused, using internal/config)
- Service package (merged into server)
- AI package (merged into executor)
- Debugger package (merged into skill)

### Fixed
- Fixed hardcoded context in API handlers
- Fixed unused variables in request logging middleware
- Fixed gofmt formatting issues

## [0.1.0] - 2024-01-01

### Added
- Initial release
- Event-driven architecture
- Priority scheduling
- Task dependencies
- WASM sandbox
- Self-healing mechanisms
- Workflow engine
- Security (RBAC, rate limiting)
- Observability (metrics, tracing)
- 6 CLI tools (loopworker, loopctl, loopdebug, loopwatch, loopbench, loopsim)
- 21 packages with comprehensive tests
- Integration tests
- Race detection tests

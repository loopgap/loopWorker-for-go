# Contributing to LoopWorker

Thank you for your interest in contributing to LoopWorker! This document provides guidelines and instructions for contributing.

## Getting Started

1. **Fork** the repository on GitHub
2. **Clone** your fork locally:
   ```bash
   git clone https://github.com/<your-username>/loopWorker-for-go.git
   cd loopWorker-for-go
   ```
3. **Create a branch** for your feature or fix:
   ```bash
   git checkout -b feature/your-feature-name
   ```

## Development

### Prerequisites

- Go 1.21+
- Node.js 18+ (for web UI)
- (Optional) Rust + wasm-pack (for WASM plugins)

### Building

```bash
# Build the main binary
make build

# Build everything
make build-all
```

### Running Tests

```bash
# Run all tests
make test

# Run with coverage
make test-cover

# Run with race detector
make test-race

# Run integration tests
make test-integration

# Run benchmarks
make bench
```

### Code Quality

Before submitting a PR, ensure your code passes all checks:

```bash
make check   # runs fmt + vet + test
```

## Project Structure

```
loopWorker-for-go/
├── cmd/loopworker/      # Main application entry point
├── pkg/                 # Public packages (importable)
│   ├── api/             # REST API handlers
│   ├── config/          # Configuration parsing
│   ├── dashboard/       # Web dashboard
│   ├── debugger/        # Debug utilities
│   ├── event/           # Event bus system
│   ├── generator/       # Task generator
│   ├── plugin/          # Plugin management
│   ├── research/        # Research module
│   ├── security/        # Auth & rate limiting
│   ├── server/          # HTTP server
│   ├── service/         # Service layer
│   ├── skill/           # Skill management
│   ├── ui/              # Terminal UI themes
│   ├── utils/           # Shared utilities
│   └── workflow/        # Workflow engine
├── internal/            # Private packages
│   ├── config/          # Internal config
│   └── core/            # Core engine
│       ├── dispatcher/  # Event dispatcher
│       ├── executor/    # Task executor
│       ├── observer/    # Observability
│       ├── sandbox/     # WASM sandbox
│       ├── scheduler/   # Task scheduler
│       └── selfheal/    # Self-healing
├── integration/         # Integration tests
├── web/canvas/          # Web UI (React + Vite)
├── examples/            # Example code
├── docs/                # Documentation
└── plugins/             # Plugin directory
```

## Commit Guidelines

- Use clear, descriptive commit messages
- Start with a verb in imperative mood (e.g., "Add feature", "Fix bug")
- Reference issues when applicable (e.g., "Fix #123")
- Keep commits focused and atomic

### Commit Message Format

```
<type>(<scope>): <subject>

<body>

<footer>
```

**Types:** `feat`, `fix`, `docs`, `style`, `refactor`, `test`, `chore`, `perf`

**Examples:**
```
feat(scheduler): add priority-based task queuing
fix(sandbox): resolve memory leak in WASM runtime
docs(readme): update API endpoint documentation
test(dispatcher): add concurrency stress tests
```

## Pull Request Process

1. Update documentation if needed
2. Add tests for new functionality
3. Ensure all tests pass (`make check`)
4. Update the README if applicable
5. Submit your PR with a clear description

## Code Style

- Follow standard Go conventions (`gofmt`, `go vet`)
- Use meaningful variable and function names
- Add comments for exported functions and complex logic
- Keep functions focused and reasonably sized

## Reporting Issues

- Use GitHub Issues for bug reports and feature requests
- Include steps to reproduce for bugs
- Include Go version and OS information

## License

By contributing, you agree that your contributions will be licensed under the MIT License.

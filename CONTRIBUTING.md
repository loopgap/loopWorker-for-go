# Contributing to LoopWorker

Thank you for your interest in contributing to LoopWorker! This document provides guidelines and instructions for contributing.

## Code of Conduct

Please be respectful and inclusive in all interactions.

## How to Contribute

### Reporting Issues

- Use the GitHub issue tracker
- Include a clear description of the issue
- Include steps to reproduce
- Include expected vs actual behavior
- Include Go version and OS information

### Submitting Changes

1. Fork the repository
2. Create a feature branch (`git checkout -b feature/amazing-feature`)
3. Make your changes
4. Run tests (`make test`)
5. Run linter (`make lint`)
6. Format code (`make fmt`)
7. Commit your changes (`git commit -m 'feat: add amazing feature'`)
8. Push to the branch (`git push origin feature/amazing-feature`)
9. Open a Pull Request

### Commit Message Format

We follow the [Conventional Commits](https://www.conventionalcommits.org/) specification:

```
<type>(<scope>): <description>

[optional body]

[optional footer]
```

Types:
- `feat`: New feature
- `fix`: Bug fix
- `docs`: Documentation changes
- `style`: Code style changes (formatting, etc.)
- `refactor`: Code refactoring
- `test`: Adding or updating tests
- `chore`: Maintenance tasks

### Code Style

- Follow Go conventions and best practices
- Use `gofmt` for formatting
- Use `go vet` for static analysis
- Write meaningful variable and function names
- Add comments for exported functions and types
- Keep functions focused and small

### Testing

- Write unit tests for new functionality
- Ensure all tests pass (`make test`)
- Run race detection (`make test-race`)
- Aim for good test coverage

### Documentation

- Update README.md if needed
- Add comments to exported functions
- Update API documentation if changing endpoints
- Add examples for new features

## Development Setup

### Prerequisites

- Go 1.21 or later
- Git
- Make (optional, but recommended)

### Building

```bash
# Build all binaries
make build

# Run tests
make test

# Run linter
make lint

# Format code
make fmt
```

### Running

```bash
# Run server
make run

# Or directly
go run ./cmd/loopworker/
```

## Release Process

1. Update CHANGELOG.md
2. Update version in version/version.go
3. Create a git tag
4. Push tag to trigger CI/CD

## Questions?

Feel free to open an issue for any questions about contributing.

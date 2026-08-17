# LoopWorker Quick Start

## Installation

### From Source

```bash
# Clone the repository
git clone https://github.com/your-org/loopworker.git
cd loopworker

# Build
make build

# Install
make install
```

### Using Docker

```bash
# Build Docker image
make docker

# Run
docker run -p 19527:19527 loopworker:latest
```

## Quick Start

### 1. Start the Server

```bash
# Start with default configuration
loopworker

# Start with custom configuration
loopworker --config /path/to/config.yaml

# Start with custom port
loopworker --port 8080
```

### 2. Create a Task

```bash
# Create a task using loopctl
loopctl task create --type echo --input "Hello, World!"

# List tasks
loopctl task list

# Get task details
loopctl task get <task-id>
```

### 3. Monitor the Server

```bash
# Watch server status
loopwatch --server http://localhost:19527

# Get server metrics
loopctl metrics

# Get server logs
loopctl logs
```

### 4. Debug Issues

```bash
# Inspect a task
loopdebug task inspect <task-id>

# Trace task execution
loopdebug task trace <task-id>

# Run diagnostics
loopdebug diagnose
```

## Configuration

LoopWorker can be configured using:

1. Configuration file (`~/.loopworker/config.yaml`)
2. Environment variables (prefix: `LOOPWORKER_`)
3. Command-line flags

### Example Configuration

```yaml
server:
  port: 19527
  host: "0.0.0.0"

plugins:
  dir: "~/.loopworker/plugins"

data:
  dir: "~/.loopworker/data"
```

### Environment Variables

```bash
export LOOPWORKER_PORT=8080
export LOOPWORKER_PLUGINS_DIR=/path/to/plugins
export LOOPWORKER_DATA_DIR=/path/to/data
```

## CLI Tools

| Tool | Description |
|------|-------------|
| `loopworker` | Main server |
| `loopctl` | Task and workflow management |
| `loopdebug` | Debugging and diagnostics |
| `loopwatch` | Real-time monitoring |
| `loopbench` | Performance benchmarking |
| `loopsim` | Load simulation |

## Building

```bash
# Build all binaries
make build

# Run tests
make test

# Run tests with race detection
make test-race

# Format code
make fmt

# Run linter
make lint

# Cross-compile for all platforms
make build-all

# Build Docker image
make docker
```

## Docker

```bash
# Build image
docker build -t loopworker .

# Run container
docker run -d \
  -p 19527:19527 \
  -v /path/to/data:/data \
  loopworker

# Run with custom configuration
docker run -d \
  -p 19527:19527 \
  -v /path/to/config.yaml:/app/config.yaml \
  loopworker --config /app/config.yaml
```

## API Reference

### Health Check

```bash
curl http://localhost:19527/api/v1/health
```

### Create Task

```bash
curl -X POST http://localhost:19527/api/v1/tasks \
  -H "Content-Type: application/json" \
  -d '{"type": "echo", "input": "Hello, World!"}'
```

### List Tasks

```bash
curl http://localhost:19527/api/v1/tasks?limit=10
```

### Get Task

```bash
curl http://localhost:19527/api/v1/tasks/<task-id>
```

## Support

- Documentation: [docs/](docs/)
- Issues: [GitHub Issues](https://github.com/your-org/loopworker/issues)

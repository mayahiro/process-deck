# Process Deck

Process Deck is a lightweight YAML-based process supervisor with a built-in TUI for local development.

It is designed for developers who want to start and monitor several local processes without introducing containers or a large orchestration layer.

## Installation

Installation packaging is not published yet. For local development, run the CLI with Go:

```sh
go run ./cmd/procdeck --dry-run --config examples/process-deck.yaml
```

## Quick Start

Create a `process-deck.yaml` file:

```yaml
version: 1

project: demo

processes:
  api:
    cmd: "npm run dev"
    cwd: "./api"
    env:
      PORT: "3000"

  worker:
    exec:
      - "python"
      - "worker.py"
    cwd: "./worker"
    depends_on:
      - api
```

Validate it and inspect the startup plan:

```sh
go run ./cmd/procdeck --dry-run --config process-deck.yaml
```

## Configuration

Process Deck uses schema `version: 1`. Each process must define exactly one of `cmd` or `exec`.

- `cmd` runs through the platform shell.
- `exec` runs an executable directly without shell expansion.
- `depends_on` waits for listed processes to reach the running state before starting the dependent process.
- `restart` supports `no`, `on-failure`, and `always`.

## Planned Keybindings

The MVP TUI is planned around these keys:

| Key | Action |
|---|---|
| `up` / `k` | Move selection up |
| `down` / `j` | Move selection down |
| `s` | Stop selected process |
| `a` | Start selected process |
| `r` | Restart selected process |
| `f` | Toggle log follow |
| `q` / `ctrl+c` | Quit and stop all processes |

## Non-goals

The MVP does not aim to provide full process-compose compatibility, container support, a REST API, server/client mode, namespaces, replicas, scheduled processes, dynamic config editing, health checks, PTY support, log rotation, metrics, or daemonization.

## Process Compose Comparison

Process Compose is a broader process orchestration tool. Process Deck intentionally targets a smaller local development workflow:

```text
define processes -> start them -> see logs/status -> stop/restart safely
```

## License

MIT

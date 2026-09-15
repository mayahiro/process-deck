# Configuration

[日本語](configuration_ja.md)

Process Deck reads a YAML config file that describes the local processes to supervise.

When `--config` is not provided, Process Deck looks for the first matching file in the current working directory:

1. `process-deck.yaml`
2. `process-deck.yml`
3. `procdeck.yaml`
4. `procdeck.yml`

The current schema version is `1`.

Normal startup reconnects to an existing session for the same working directory and config path, if one exists. That session keeps its original parsed configuration and inherited environment; the file is only loaded when creating a new session. `--dry-run` always validates the file currently on disk. See [Reconnecting after a crash](../README.md#reconnecting-after-a-crash) for session lifetime and recovery limits.

## Example

```yaml
version: 1

project: demo

defaults:
  restart: "no"
  backoff: "1s"
  stop_signal: "TERM"
  stop_timeout: "5s"
  log_buffer_lines: 500
  pty: false

processes:
  api:
    cmd: "npm run dev"
    cwd: "./api"
    env_file:
      - ".env"
    env:
      PORT: "3000"

  worker:
    exec:
      - "python"
      - "worker.py"
    cwd: "./worker"
    depends_on:
      - api
    restart: "on-failure"
```

## Top-level fields

| Field | Required | Type | Description |
|---|---:|---|---|
| `version` | Yes | integer | Schema version. Must be `1`. |
| `project` | No | string | Display name for the project. If omitted, Process Deck uses the current directory name. |
| `defaults` | No | object | Default process options. Individual process fields override these values. |
| `processes` | Yes | object | Map of process names to process definitions. At least one process is required. |

Unknown YAML fields are rejected.

## Default fields

`defaults` supports the same runtime options as a process, except command, directory, environment, and dependencies.

| Field | Type | Default | Description |
|---|---|---|---|
| `restart` | string | `no` | Default restart policy. Must be `no`, `on-failure`, or `always`. |
| `backoff` | duration | `1s` | Delay before restarting a process. Uses Go duration syntax such as `500ms`, `1s`, or `2m`. |
| `stop_signal` | string | `TERM` | Signal sent when stopping a process. Must be `TERM`, `INT`, `KILL`, `HUP`, or `QUIT`; `SIG` prefixes are also accepted. |
| `stop_timeout` | duration | `10s` | Time to wait after `stop_signal` before sending `KILL`. Uses Go duration syntax. |
| `log_buffer_lines` | integer | `1000` | Number of in-memory log lines retained per process. Set `0` to disable retention. |
| `pty` | boolean | `false` | Run processes with a pseudo terminal by default. PTY output is captured as a single `pty` stream. |

## Process fields

Each process must define exactly one of `cmd` or `exec`.

| Field | Required | Type | Description |
|---|---:|---|---|
| `cmd` | One of `cmd` or `exec` | string | Command string executed through `/bin/sh -c`. Shell expansion, pipes, and redirects are handled by the shell. |
| `exec` | One of `cmd` or `exec` | string array | Executable and arguments executed directly without shell expansion. The first item must be the executable name or path. |
| `cwd` | No | string | Working directory for the process. Relative paths are resolved from the directory where `procdeck` was started, not from the config file location. |
| `env_file` | No | string or string array | Environment file paths loaded before `env`. Relative paths are resolved from the process `cwd`. |
| `env` | No | object | Environment variables added to the inherited `procdeck` environment. Keys must not be empty and must not contain `=`. |
| `depends_on` | No | string array | Process names that must reach `running` before this process starts. |
| `restart` | No | string | Process restart policy. Overrides `defaults.restart`. |
| `backoff` | No | duration | Process restart delay. Overrides `defaults.backoff`. |
| `stop_signal` | No | string | Process stop signal. Overrides `defaults.stop_signal`. |
| `stop_timeout` | No | duration | Process stop timeout. Overrides `defaults.stop_timeout`. |
| `log_buffer_lines` | No | integer | Process log buffer size. Set `0` to disable retention for this process. |
| `pty` | No | boolean | Run this process with a pseudo terminal. This helps TTY-aware tools emit color and terminal output, but stdout and stderr are merged into the `pty` stream. Overrides `defaults.pty`. |

## Commands

Use `cmd` for shell syntax:

```yaml
processes:
  api:
    cmd: "npm run dev 2>&1 | tee api.log"
```

Use `exec` when you want direct execution without shell parsing:

```yaml
processes:
  worker:
    exec:
      - "python"
      - "worker.py"
```

Use `pty` when a command needs to detect a terminal, for example to emit color:

```yaml
processes:
  api:
    cmd: "npm run dev"
    pty: true
```

## Working directory and environment

`cwd` only sets the child process working directory.

Process Deck itself does not evaluate shell hooks, `mise activate`, or `direnv` files for each process `cwd`. Each process inherits the environment of the `procdeck` process, then receives variables from `env_file` and its `env` block.

If you start `procdeck` from a shell where `mise` or `direnv` has already activated the current directory, that activated environment is inherited by managed processes. Changing a process `cwd` does not trigger a new activation for that directory.

## Environment files

`env_file` loads one or more environment files into the child process environment:

```yaml
processes:
  api:
    cmd: "bundle exec rails s"
    cwd: "./api"
    env_file:
      - ".env"
      - ".env.local"
    env:
      PORT: "3000"
```

The precedence order is:

1. Environment inherited by `procdeck`
2. `env_file` entries, processed from top to bottom
3. `env`, applied last

Relative `env_file` paths are resolved from the process `cwd`. Missing files are errors. Process Deck does not automatically load `.env`; the file must be listed in `env_file`.

The parser intentionally supports a small dotenv subset:

- Blank lines and lines beginning with `#` are ignored.
- Lines without `=` are ignored.
- Variable lines use `KEY=VALUE` or `export KEY=VALUE`.
- Empty values such as `KEY=` are allowed.
- Single-quoted and double-quoted values are unquoted.
- Inline comments in unquoted values are ignored when `#` is preceded by a space or tab, including `KEY= # comment`.
- A `#` without preceding whitespace remains part of an unquoted value, as in `KEY=value#fragment`.
- Comments after a closing quote are ignored.
- An empty key or malformed quoted value is an error.
- Shell syntax other than the optional `export` prefix is not evaluated.
- Variable interpolation such as `${OTHER}` is not supported.

`env_file` may also be written as a single string:

```yaml
processes:
  api:
    cmd: "npm run dev"
    cwd: "./api"
    env_file: ".env"
```

## External environment managers

If a command is resolved through a `mise` shim, `mise` may apply its own config using the process working directory. For explicit and portable config, wrap the command:

```yaml
processes:
  api:
    cmd: "mise exec -- npm run dev"
    cwd: "./api"

  worker:
    cmd: "direnv exec . python worker.py"
    cwd: "./worker"
```

The same wrappers can be used with `exec`:

```yaml
processes:
  api:
    exec:
      - "mise"
      - "exec"
      - "--"
      - "npm"
      - "run"
      - "dev"
    cwd: "./api"
```

## Dependencies

`depends_on` defines startup ordering only.

Process Deck validates that dependencies reference known processes, do not point to the process itself, do not contain duplicates, and do not form cycles.

A dependent process starts after all listed dependencies reach `running`. If a dependency fails before the dependent process starts, the dependent process is skipped. Stopping a process also stops processes that depend on it.

## Restart policy

| Value | Behavior |
|---|---|
| `no` | Never restart after exit. |
| `on-failure` | Restart only when the exit code is non-zero. |
| `always` | Restart after any exit. |

Manual stops suppress automatic restart.

## Stopping processes

Process Deck currently targets macOS.

Processes are started in their own process group. When a process is stopped, Process Deck sends `stop_signal` to the process group, waits for `stop_timeout`, then sends `KILL` if the process group is still running.

## Logs

Process Deck captures stdout and stderr line by line. The background supervisor keeps an in-memory ring buffer per process, including while no client is attached. In `--no-tui` mode, log lines are written to stdout with the process name and stream. Reconnecting restores retained history up to `log_buffer_lines`; older lines and history with retention disabled cannot be recovered.

When `pty: true` is enabled, stdout and stderr are connected to the same pseudo terminal and cannot be separated. Those log lines use the `pty` stream label.

The TUI labels each log line with the captured stream, such as `[stdout]`, `[stderr]`, or `[pty]`. Log wrapping is enabled by default and can be toggled with `w`. Logs can be scrolled vertically with `pgup` and `pgdn`; horizontal scrolling is available with `left` and `right` when wrapping is disabled.

## Validation

Use `--dry-run` to validate the config and print the startup plan:

```sh
procdeck --dry-run --config process-deck.yaml
```

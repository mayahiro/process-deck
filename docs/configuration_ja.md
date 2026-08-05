# 設定

[English](configuration.md)

Process Deck は監視対象のローカル process を記述した YAML 設定ファイルを読み込みます

`--config` を指定しない場合、Process Deck は現在の作業ディレクトリで最初に一致するファイルを使用します:

1. `process-deck.yaml`
2. `process-deck.yml`
3. `procdeck.yaml`
4. `procdeck.yml`

現在の schema version は `1` です

## 例

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

## Top-level field

| Field | 必須 | Type | 説明 |
|---|---:|---|---|
| `version` | Yes | integer | Schema version、`1` である必要がある |
| `project` | No | string | project の表示名、省略時は現在のディレクトリ名を使用 |
| `defaults` | No | object | process option の既定値、個々の process field が上書きする |
| `processes` | Yes | object | process 名から process 定義への map、1つ以上必要 |

未知の YAML field は拒否されます

## Default field

`defaults` は command、directory、environment、dependency を除く process と同じ runtime option に対応します

| Field | Type | 既定値 | 説明 |
|---|---|---|---|
| `restart` | string | `no` | restart policy の既定値、`no`、`on-failure`、`always` のいずれか |
| `backoff` | duration | `1s` | process を再起動するまでの待機時間、`500ms`、`1s`、`2m` などの Go duration syntax を使用 |
| `stop_signal` | string | `TERM` | process 停止時に送る signal、`TERM`、`INT`、`KILL`、`HUP`、`QUIT` に対応し、`SIG` prefix も使用可能 |
| `stop_timeout` | duration | `10s` | `stop_signal` の後に `KILL` を送るまでの待機時間、Go duration syntax を使用 |
| `log_buffer_lines` | integer | `1000` | process ごとに memory へ保持する log 行数、`0` で保持を無効化 |
| `pty` | boolean | `false` | process を pseudo terminal で実行する既定値、PTY output は単一の `pty` stream として取得 |

## Process field

各 process には `cmd` または `exec` のいずれか一方を定義する必要があります

| Field | 必須 | Type | 説明 |
|---|---:|---|---|
| `cmd` | `cmd` または `exec` の一方 | string | `/bin/sh -c` を通して実行する command string、shell expansion、pipe、redirect は shell が処理 |
| `exec` | `cmd` または `exec` の一方 | string array | shell expansion なしで直接実行する executable と引数、最初の要素は executable 名または path |
| `cwd` | No | string | process の作業ディレクトリ、相対 path は設定ファイルの場所ではなく `procdeck` の起動ディレクトリを基準に解決 |
| `env_file` | No | string または string array | `env` より前に読み込む environment file path、相対 path は process の `cwd` を基準に解決 |
| `env` | No | object | 継承した `procdeck` environment へ追加する environment variable、key は空にできず `=` を含められない |
| `depends_on` | No | string array | この process の起動前に `running` になる必要がある process 名 |
| `restart` | No | string | process の restart policy、`defaults.restart` を上書き |
| `backoff` | No | duration | process の restart delay、`defaults.backoff` を上書き |
| `stop_signal` | No | string | process の stop signal、`defaults.stop_signal` を上書き |
| `stop_timeout` | No | duration | process の stop timeout、`defaults.stop_timeout` を上書き |
| `log_buffer_lines` | No | integer | process の log buffer size、`0` でこの process の保持を無効化 |
| `pty` | No | boolean | process を pseudo terminal で実行、TTY 対応 tool の色や terminal output に有用だが stdout と stderr は `pty` stream に統合、`defaults.pty` を上書き |

## コマンド

shell syntax を使用する場合は `cmd` を使います:

```yaml
processes:
  api:
    cmd: "npm run dev 2>&1 | tee api.log"
```

shell parsing を行わず直接実行する場合は `exec` を使います:

```yaml
processes:
  worker:
    exec:
      - "python"
      - "worker.py"
```

command が terminal を検出する必要がある場合は `pty` を使います:

```yaml
processes:
  api:
    cmd: "npm run dev"
    pty: true
```

## 作業ディレクトリと環境変数

`cwd` は child process の作業ディレクトリだけを設定します

Process Deck 自体は process ごとの `cwd` に対して shell hook、`mise activate`、direnv file を評価しません
各 process は `procdeck` process の environment を継承し、`env_file` と `env` block の変数を受け取ります

`mise` または direnv が現在のディレクトリで有効になっている shell から `procdeck` を起動すると、その有効化済み environment は管理対象 process に継承されます
process の `cwd` を変更しても、そのディレクトリに対する新しい activation は実行されません

## Environment file

`env_file` は1つ以上の environment file を child process environment へ読み込みます:

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

優先順位は次のとおりです:

1. `procdeck` が継承した environment
2. 上から下へ処理した `env_file` entry
3. 最後に適用する `env`

相対 `env_file` path は process の `cwd` を基準に解決されます
存在しないファイルはエラーになります
Process Deck は `.env` を自動的に読み込まないため、`env_file` への明示が必要です

parser は意図的に小さな dotenv subset だけに対応します:

- 空行と `#` で始まる行は無視
- `=` のない行は無視
- variable 行は `KEY=VALUE` または `export KEY=VALUE` を使用
- `KEY=` のような空の value を許可
- single quote と double quote の value は quote を除去
- unquoted value では空白またはtabに続く `#` 以降を inline comment として無視し、`KEY= # comment` にも対応
- unquoted value の前に空白がない `#` は `KEY=value#fragment` のように value の一部として保持
- 閉じ quote 後の comment は無視
- 空の key または不正な quoted value はエラー
- 省略可能な `export` prefix 以外の shell syntax は評価しない
- `${OTHER}` のような variable interpolation は未対応

`env_file` は単一の string としても記述できます:

```yaml
processes:
  api:
    cmd: "npm run dev"
    cwd: "./api"
    env_file: ".env"
```

## 外部 environment manager

command が `mise` shim を通して解決される場合、`mise` は process の作業ディレクトリを使って独自の設定を適用する場合があります
明示的で移植可能な設定にするには command を wrapper で囲みます:

```yaml
processes:
  api:
    cmd: "mise exec -- npm run dev"
    cwd: "./api"

  worker:
    cmd: "direnv exec . python worker.py"
    cwd: "./worker"
```

同じ wrapper は `exec` でも使用できます:

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

## 依存関係

`depends_on` は起動順だけを定義します

Process Deck は依存先が既知の process を参照していること、自分自身を参照していないこと、重複がないこと、cycle がないことを検証します

依存 process は、指定された全依存先が `running` になった後で起動します
依存先が起動前に失敗した場合、依存 process は skip されます
process を停止すると、その process に依存する process も停止します

## Restart policy

| 値 | 挙動 |
|---|---|
| `no` | 終了後に再起動しない |
| `on-failure` | exit code が0以外の場合だけ再起動 |
| `always` | 終了時に常に再起動 |

手動停止では自動再起動を行いません

## Process の停止

Process Deck は現在 macOS を対象としています

process は個別の process group で起動されます
process を停止すると、Process Deck は process group へ `stop_signal` を送り、`stop_timeout` の間待機してから、process group が動作中であれば `KILL` を送ります

## Log

Process Deck は stdout と stderr を行単位で取得します
TUI は process ごとの memory ring buffer を保持します
`--no-tui` mode では、process 名と stream を付けて log 行を stdout へ出力します

`pty: true` の場合、stdout と stderr は同じ pseudo terminal に接続されるため分離できません
これらの log 行には `pty` stream label が付きます

TUI は各 log 行に `[stdout]`、`[stderr]`、`[pty]` などの取得元 stream を表示します
log wrap は既定で有効であり、`w` で切り替えられます
`pgup` と `pgdn` で log を垂直方向へ scroll でき、wrap 無効時は `left` と `right` で水平方向へ scroll できます

## 検証

`--dry-run` で設定を検証し、起動計画を表示します:

```sh
procdeck --dry-run --config process-deck.yaml
```

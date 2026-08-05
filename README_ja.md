# Process Deck

[English](README.md)

Process Deck はローカル開発向けの組み込み TUI を備えた、軽量な YAML ベースの process supervisor です

container や大規模な orchestration layer を導入せず、複数のローカル process を起動して監視したい開発者を対象としています

## インストール

ローカル binary を build します:

```sh
make build
./tmp/procdeck --version
```

## クイックスタート

`process-deck.yaml` を作成します:

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

設定を検証して起動計画を確認します:

```sh
go run ./cmd/procdeck --dry-run --config process-deck.yaml
```

TUI を起動します:

```sh
go run ./cmd/procdeck --config process-deck.yaml
```

TUI なしで実行します:

```sh
go run ./cmd/procdeck --no-tui --config process-deck.yaml
```

## 設定

Process Deck は schema `version: 1` を使用します
各 process には `cmd` または `exec` のいずれか一方を定義する必要があります

- `cmd` は `/bin/sh -c` を通して実行されます
- `exec` は shell expansion を行わず executable を直接実行します
- `env_file` は process の `cwd` を基準に1つ以上の environment file を読み込み、entry は `KEY=VALUE` または `export KEY=VALUE` を使用でき、省略可能な inline `#` comment を記載できます
- `depends_on` は指定された process が `running` になるまで依存 process の起動を待機します
- `restart` は `no`、`on-failure`、`always` に対応します
- `stop_signal` の既定値は `TERM` です
- `stop_timeout` の既定値は `10s` です
- `log_buffer_lines` は memory に保持する process ごとの log 行数を指定し、`0` で保持を無効化します
- `pty` は pseudo terminal で process を実行し、TTY 対応 tool が色を出力する場合に役立ちますが、stdout と stderr は `pty` stream に統合されます

Process Deck は現在 macOS を対象としています

YAML schema、field の意味、environment manager に関する注意事項は [設定](docs/configuration_ja.md) を参照してください

## コマンド

```sh
make fmt
make test
make test-race
make build
make release-darwin-arm64 VERSION=0.1.0
make release-darwin-amd64 VERSION=0.1.0
```

release build は binary を `tmp/` に出力し、`procdeck --version` が表示する version を埋め込みます

## キーバインド

| Key | Action |
|---|---|
| `up` / `k` | 選択を上へ移動 |
| `down` / `j` | 選択を下へ移動 |
| `s` | 選択中の process を停止 |
| `a` | 選択中の process を起動 |
| `r` | 選択中の process を再起動 |
| `f` | log follow を切替 |
| `w` | log wrap を切替 |
| `pgup` / `pgdn` | log を1 page scroll |
| `ctrl+u` / `ctrl+d` | log を半 page scroll |
| `home` / `end` | log の先頭または末尾へ移動 |
| `left` / `right` | wrap 無効時に log を水平方向へ scroll |
| `q` / `ctrl+c` | 終了して全 process を停止 |

## 対象外

MVP では container 対応、REST API、server/client mode、namespace、replica、scheduled process、動的な設定編集、health check、対話的な PTY input forwarding、log rotation、metrics、daemonization を対象としません

## ライセンス

MIT

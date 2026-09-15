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

## クラッシュ後の再接続

同じ作業ディレクトリから、同じ設定ファイルを指定して再度起動します:

```sh
procdeck --config process-deck.yaml
```

Process Deck は既存のセッションへ自動で再接続します
独立したバックグラウンドの supervisor が process、出力 pipe、PTY を保持するため、terminal client のクラッシュや `SIGKILL` 後も process を管理できます
client の切断後も PID、再起動ポリシー、保持中のログ履歴が維持され、再接続後に停止・起動・再起動を操作できます

- 同時に接続できる client は1つで、2つ目の client は process を重複起動せずエラーになります
- 再接続ではセッション起動時の YAML 設定と継承環境を使い、それらの変更を反映するにはセッションを終了して起動し直します。従来どおり、`env_file` の内容は managed process の起動時に読み込みます
- 稼働中のセッションは開始時の supervisor のバージョンを使い続けます。`procdeck` を更新した後は、既存のセッションを終了して新しく起動すると修正が反映されます
- `q`、`Ctrl+C`、`SIGTERM` は全 process を停止してセッションを終了します。terminal error や client 接続の消失では、再接続できる状態を維持します
- 最初に `--no-tui` で起動したセッションは全 process の終了時に終了し、最初に TUI で起動したセッションは切断中も含めて明示的に停止するまで残ります
- ログ履歴は `log_buffer_lines` の範囲で保持し、`0` では履歴を復元しません。headless で再接続すると保持中の履歴を出力してから新しいログを表示します。ログはメモリ上に保持し、ディスクには保存しません
- 復元にはバックグラウンドの supervisor が生きている必要があり、旧バージョンで迷子になった process、supervisor 自体のクラッシュ、OS 再起動からの復元には対応しません。supervisor がクラッシュした場合は重複起動せず stale socket のエラーを表示するため、残存 process を停止してからエラーに示された socket を削除し、新しいセッションを起動します

セッションは `/tmp` 配下の所有者専用ディレクトリとローカルの Unix socket を使用します
`--dry-run` はセッションの起動や接続を行わず、ディスク上の設定ファイルを検証します

## 設定

Process Deck は schema `version: 1` を使用します
各 process には `cmd` または `exec` のいずれか一方を定義する必要があります

- `cmd` は `/bin/sh -c` を通して実行されます
- `exec` は shell expansion を行わず executable を直接実行します
- `env_file` は process の `cwd` を基準に1つ以上の environment file を読み込み、entry は `KEY=VALUE` または `export KEY=VALUE` を使用でき、省略可能な inline `#` comment を記載できます
- `depends_on` は指定された process が `running` になるまで依存 process の起動を待機します。手動停止は依存する全 process も停止し、手動再起動は操作前に稼働していた依存 process を依存順に復帰させます
- `restart` は `no`、`on-failure`、`always` に対応します
- `stop_signal` の既定値は `TERM` です
- `stop_timeout` の既定値は `10s` です
- `log_buffer_lines` は memory に保持する process ごとの log record 数を指定し、`0` で保持を無効化します。1 MiBを超える行はサイズを制限した record へ分割します
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
| `s` | 選択中の process と依存する process を停止 |
| `a` | 選択中の process を起動 |
| `r` | 選択中の process と操作前に稼働していた依存 process を再起動 |
| `f` | log follow を切替 |
| `w` | log wrap を切替 |
| `pgup` / `pgdn` | log を1 page scroll |
| `ctrl+u` / `ctrl+d` | log を半 page scroll |
| `home` / `end` | log の先頭または末尾へ移動 |
| `left` / `right` | wrap 無効時に log を水平方向へ scroll |
| `q` / `ctrl+c` | 終了して全 process を停止 |

terminal が `Super`（Command を含む）または `Hyper` 修飾キーを通知した場合、そのキーの組み合わせでは Process Deck のショートカットを実行しません

## 対象外

Process Deck では container 対応、REST API、remote process management、namespace、replica、scheduled process、動的な設定編集、health check、対話的な PTY input forwarding、log rotation、metrics、ログインやOS起動時に開始する system service を対象としません

## ライセンス

MIT

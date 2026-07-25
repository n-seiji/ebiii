# ebiii

ebiii は、Slack の mention を受けて Codex が方針を検討し、必要な作業を別セッションで実行して同じスレッドへ結果を返す、ミニマムな連携ボットです。

## Slack App の準備

1. Slack App を作成し、Socket Mode を有効にします。
2. Bot Token Scopes に `app_mentions:read`、`chat:write`、`reactions:write` を追加します。
3. Event Subscriptions で `app_mention` を購読します。
4. Workspace に App をインストールして Bot Token (`xoxb-...`) を取得します。
5. Socket Mode 用の App Token (`xapp-...`) を取得します。

## 設定と起動

`.env.example` を `.env` にコピーし、Slack token、許可する user/channel ID などを設定します。

```sh
cp .env.example .env
mise run build   # または: go build -o ebiii ./cmd/ebiii
./ebiii
```

Go のバージョンは [mise](https://mise.jdx.dev/) で管理しています(`mise install` で揃います)。テストは `mise run test`、lint は `mise run lint` で実行できます。mise なしでも `go build` / `go test -race ./...` / `go vet ./...` で同等です。

ebiii は単一プロセスでの運用を前提としており、多重起動には対応していません。

## interrupted の運用

work turn は非冪等な副作用を持つ可能性があります。そのため、work 中のクラッシュや失敗は `interrupted` として記録し、自動再実行しません。ユーザーが作業状況を確認したうえで、新しい mention として依頼し直してください。

# ebiii 設計ドラフト v9(sol レビュー8回目反映)

## キーの定義(混同禁止)
- **eventKey** = `channel:mention_ts`(mention メッセージ自身の ts)。events.json / ClaimEvent / Transition はすべてこのキー。mention 1 件 = 1 エントリなので、同一スレッド内の 2 件目以降の mention は独立に処理される
- **threadKey** = `channel:(thread_ts||ts)`。sessions.json(codex threadID の resume)とスレッド mutex にのみ使用

## 目的
ebiclaw のミニマム版。Slack mention → codex で方針検討 → 別セッションで作業実行 → 同スレッドに結果返信。Web UI なし、env のみで設定。

## 技術選定
- Go 1.25 / slack-go/slack(Socket Mode)
- codex CLI サブプロセス(`codex exec --json` / `codex exec resume`)。引数仕様・JSONL パースは ebiclaw pkg/codexpipe/exec.go を移植し、exec_test.go 相当の引数テストも持ち込む

## ディレクトリ構成
```
ebiii/
├── cmd/ebiii/main.go
├── internal/
│   ├── config/config.go     # env 読み込み。EBIII_HOME は起動時に絶対パス化。allowlist の空要素除去・形式検査・空なら fail-fast
│   ├── slackbot/bot.go      # socketmode, AppMention, reaction, thread reply
│   ├── codex/runner.go      # exec 実行・JSONL パース・timeout・出力上限
│   └── state/store.go       # sessions.json(thread→threadID)と events.json(メッセージ状態)を別ファイルで管理
├── workspace/  memory/  playbooks/
├── .env.example
└── Makefile
```

## codex Runner API(契約を明確化)
```go
type TurnResult struct {
    ThreadID  string
    Messages  []string // 全 agent_message(完了順)
    Completed bool     // turn.completed を観測し turn.failed が無い
    Err       string   // turn.failed / error の内容
}
Run(ctx, threadID, sandbox, cwd string, writableRoots []string, prompt string,
    onThreadStarted func(id string) error) (*TurnResult, error)
// ストリーミング投稿はしないため onMessage コールバックは設けない(TurnResult.Messages のみ利用)
```
- **成功条件**: `Completed == true` のみを成功とする(ebiclaw の「message 1 件あれば成功」規約は採らない)
- **空出力の扱い**: Completed=true かつ Messages が空の場合 — plan turn は出力契約パース失敗と同じ fail-closed 経路、work turn は結果を確認できないため **interrupted** 扱い(⚠️ + ❌)。Runner/bot テストにこのケースを含める
- `onThreadStarted func(id string) error`: `thread.started` 受信時に即発火 → store へ保存。**非 nil エラーを返したら Runner が子プロセスを cancel して turn 中止**(孤児セッション防止)。このとき Run はそのエラーを wrap して返し、呼び出し側は state を failed(plan 時)に遷移させる。**sessions.json に保存するのは plan turn の threadID のみ**。work turn は使い捨てセッションなので `onThreadStarted` は nil(保存しない)を渡す — plan の threadID を work の ID で上書きする事故を契約で防ぐ
- resume 時は cwd 引数を無視する(codex CLI の制約でセッション作成時 cwd を引き継ぐため。新規時のみ -C を付与)
- **出力上限**: stdout 総量 64MB / stderr 16MB(超過分は切り捨て)/ agent_message 数 100 / 最終テキスト 128KB。stdout 系超過は turn 失敗扱いで子プロセス kill
- timeout(`CODEX_TIMEOUT`、既定 30m)。cancel/timeout 後は子プロセスの終了を確認してから復帰

## 状態機械(Critical 対応)
events.json に `channel:ts` → `{state, updated_at}` を永続化。状態は
`received → planning → plan_posted → working → done | failed | interrupted`(NONE の場合は plan_posted → done、fail-closed の場合は planning → done)。キーは eventKey
- 遷移表(これ以外の遷移は Transition がエラー): received→planning / planning→plan_posted|done(fail-closed)|failed / plan_posted→working|done(NONE)|failed / working→done|interrupted / failed→received(再claim)
- **failed = 再実行可能(work 未着手の失敗のみ)/ interrupted = 再実行不可(work が副作用を持った可能性がある失敗)** と定義。working 以降のあらゆる失敗(work 失敗・結果投稿のリトライ後失敗・クラッシュ)は interrupted にし、⚠️(投稿できる場合)+ ❌ で通知。再依頼は新規 mention で行う
- **store 内部に store 専用 mutex** を持ち、read-modify-write を全スレッド横断で直列化(sessions.json / events.json とも。スレッド単位ロックとは別)
- **原子的 claim API**: `ClaimEvent(key) (claimed bool, err error)` — 「未登録 or failed なら received を記録して true、それ以外は false」を store mutex 内で一括実行。永続化失敗時はメモリ上の変更をロールバックして claimed=false + err を返す。イベント受信時はこの claim に成功した goroutine だけが処理を開始する(同時配信の二重起動防止)
- **遷移 API**: `Transition(key, from, to State) error` — 期待状態付き CAS。現在状態 ≠ from ならエラー(誤順序・二重完了の検出)。**各工程は「遷移の永続化成功」を確認してから次工程を開始する**。特に work turn は `Transition(key, plan_posted, working)` の永続化成功後にのみ子プロセスを起動する(クラッシュ時に working として検出されることを保証)
- 再配信時: 進行中(received〜working)・done・interrupted は無視。failed のみ claim が通り再実行(failed→received→planning が正式遷移)
- **起動時リカバリ**: received/planning/plan_posted は `failed` に戻す(再配信・再 mention で安全にやり直せる)。**working は `interrupted`(終端)にする** — work は非冪等な副作用を持ちうるため自動再実行しない。ユーザーが状況を確認して新しい mention で依頼し直す運用(README に明記)。リカバリの永続化に失敗したら fail-fast で起動中止
- GC: 終端状態(done/failed/interrupted)のみ 7 日で削除
- 書き込みは tmp + fsync + rename(親ディレクトリ fsync まではしない=電源断の完全耐性はスコープ外と明記)
- **クラッシュ整合性の方針**: Slack 投稿 → state 保存の順とし、「重複投稿を許容、消失は許容しない」を原則とする

## 処理フロー
1. **受信したすべての envelope をただちに ACK**(内容・許可に関わらず。無視/拒否するイベントも ACK する — 再配信誘発防止)。以降の処理は goroutine で実行
2. フィルタリング: bot 自身・subtype 付き(bot_message/message_changed 等)・空テキストは無視。allowlist 検査(拒否はエラーログを残して終了)
3. `ClaimEvent(eventKey)` 照合(上記状態機械)。claimed=false なら終了。**err の場合(永続化失敗)**: ACK 済みで再配信が期待できないため、スレッドに「受付に失敗したので再 mention してほしい」旨を投稿(投稿も失敗したらエラーログ)して終了
4. 👀 付与(失敗はエラーログを残して続行)
5. **Plan turn**(sandbox=read-only, cwd=EBIII_HOME): `Transition(eventKey, received, planning)` の永続化成功後に Runner を起動。threadKey で sessions.json を引き resume/new。スレッド mutex で直列化。memory は RWMutex の read lock を取って実行(work の書き込みと排他)
   - プロンプト前置き: 絶対パスで MEMORY.md・playbook 一覧を提示、「該当 playbook を読んで従え」、出力契約の指示
   - **出力契約**: 成功 turn の最後の agent_message を `## 方針` / `## 作業指示` の 2 見出しでパース。判定規則: 行頭見出しのみ・コードフェンス内は見出しと見なさない(行頭の ``` または ~~~ 3 文字以上でフェンス開始/終了をトグル。未閉フェンスは末尾まで続くと見なす)・各見出しちょうど 1 回・この順序・両セクション非空。`## 作業指示` の本文が `NONE` 単独行なら作業なし(→方針投稿後 done)
   - **パース失敗は fail-closed**: work を起動せず、plan 全文(Messages 空の場合は固定のエラー案内のみ)をスレッドに投稿して「作業指示を確定できなかったので指示を明確にして再 mention してほしい」と伝え、`planning → done` に遷移(**planning→done を正式遷移に追加**。fail-closed 専用の終端経路)
6. 方針セクションをスレッドに投稿(→ `plan_posted`)
7. **Work turn**(作業指示 ≠ NONE): 新規セッション、sandbox=workspace-write、cwd=EBIII_HOME/workspace、writable_roots=[EBIII_HOME/memory](いずれも起動時に絶対パス化済みの値を渡す)。グローバル work mutex と memory RWMutex は分離し、常に workMu → memoryMu(write)の順で取得(デッドロック規則)。全スレッド直列化。ロックは turn 全体+子プロセス終了確認まで保持。ロック待ちは timeout に含めない(待機中である旨は投稿しない=ミニマム)
   - プロンプト: 作業指示全文 + 「重要な学びがあれば MEMORY.md に追記(なければ書かない)」
8. 結果(成功 turn の最終 message)をスレッドに返信(→ done)
9. リアクション: ✅ or ❌ を追加してから 👀 削除(失敗はエラーログを残して続行)
- 分割投稿は失敗チャンクのみリトライ(全文再送しない)

## エラーハンドリング / shutdown
- plan 失敗・方針投稿失敗(work 未着手): ⚠️ + ❌ + failed(再配信・再 mention でリトライ可)
- work 失敗(Completed=false / timeout / 上限超過)・結果投稿のリトライ後失敗: ⚠️ + ❌ + **interrupted**(自動再実行しない)。部分出力は work 指示・結果に使わない
- Slack 投稿失敗: 5xx/429 のみ 1 回リトライ(429 は Retry-After を待つ。同一内容の再送で重複投稿を許容)
- shutdown: 受付 ctx と turn ctx を分離。SIGINT → socketmode 停止(受付 ctx cancel、以後 WaitGroup.Add しない: Add は受付ループ内のみで行い、ループ停止後に Wait する構造)→ WaitGroup を 60s 待つ → 超過で turn ctx cancel → 子プロセス終了確認
- メッセージは 3900 字で分割投稿

## メモリ / Playbook(ミニマム化)
- メモリは **単一 `memory/MEMORY.md`**(topics/ はやめる)。「価値ある学びのみ追記」とプロンプトで指示
- playbooks/*.md(frontmatter name/description)。**起動時に一覧生成**(再走査しない。追加したら再起動)。走査はファイル名ソート、1 ファイル 64KB 超は一覧から除外して警告ログ
- プロンプト内でユーザー入力は `<user_message>` ブロックに隔離(命令境界の明示。主防御はあくまで fail-closed とサンドボックス)

## 設定(.env)
```
SLACK_BOT_TOKEN / SLACK_APP_TOKEN
SLACK_ALLOWED_USER_IDS=U123,U456   # 必須・U 形式検査
SLACK_ALLOWED_CHANNEL_IDS=         # 空なら全許可
CODEX_COMMAND=codex                # 実行ファイルパスのみ(引数不可)
CODEX_MODEL=
CODEX_TIMEOUT=30m
EBIII_HOME=.
```
Slack scope: app_mentions:read, chat:write, reactions:write

## テスト
- runner: 引数構築(new/resume/sandbox/writable_roots)、JSONL パース(completed/failed/error/上限超過)、onThreadStarted 発火
- 出力契約パーサ: 正常/見出し欠落/順序違い/同一見出し複数出現/空セクション/コードフェンス内見出し(未閉フェンス含む)/本文中の同名見出し(行頭以外)/NONE+余談
- state store: ClaimEvent の原子性(並行 claim で 1 件のみ成功)、起動時リカバリ(working→interrupted、他→failed、保存失敗で fail-fast)
- state store: atomic write、破損 fail-fast、状態遷移と再配信挙動、7 日 GC
- playbook 一覧: frontmatter 欠落、ソート順、サイズ超過除外
- bot: フェイク Slack で allowlist 拒否・重複 event スキップ・リアクション順序・fail-closed 経路

## やらないこと(YAGNI・確定)
- Web UI / HTTP Events API / DB / アーカイバ / Docker / worktree / 多重プロセス保護 / 429 高度制御 / キュー待ち UX 通知 / playbook ホットリロード

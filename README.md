# Aster Server

Aster Server は、Aster の REST API、WebSocket Gateway、永続データを管理する Go Backend です。
現在はPassword認証、Aster Session、Guild、Channel、Message、返信、Reaction、Invite、Role、Guild Memberの永続化とWebSocket配信、入力中通知を提供します。

> [!WARNING]
> このリポジトリは初期実装段階です。
> `0.x` の間は、互換性を保たない変更が入る可能性があります。

## 実装済みの API

API の通信契約は [Aster Protocol](https://github.com/grampr/Aster-protocol) を正とします。

| Method | Path | 説明 |
| --- | --- | --- |
| `GET` | `/api/v1/health` | HTTP Server の稼働状態を返す |
| `POST` | `/api/v1/auth/password/register` | Password Account と Session を作成する |
| `POST` | `/api/v1/auth/password/login` | Password で Session を作成する |
| `POST` | `/api/v1/auth/token/refresh` | Refresh Token を交換する |
| `POST` | `/api/v1/auth/logout` | 現在の Session を破棄する |
| `GET` | `/api/v1/users/@me` | 認証済み User 自身を返す |
| `GET, POST` | `/api/v1/guilds` | 参加Guildの一覧取得と作成 |
| `GET, PATCH, DELETE` | `/api/v1/guilds/{guild_id}` | Guildの取得、変更、削除 |
| `GET, POST` | `/api/v1/guilds/{guild_id}/channels` | Channelの一覧取得と作成 |
| `GET, PATCH, DELETE` | `/api/v1/channels/{channel_id}` | Channelの取得、変更、削除 |
| `GET, POST` | `/api/v1/channels/{channel_id}/messages` | Messageの一覧取得と投稿 |
| `GET, PATCH, DELETE` | `/api/v1/channels/{channel_id}/messages/{message_id}` | Messageの取得、編集、削除 |
| `PUT, DELETE` | `/api/v1/channels/{channel_id}/messages/{message_id}/reactions/{emoji}` | Reactionの追加と解除 |
| `POST` | `/api/v1/channels/{channel_id}/typing` | 入力開始または継続の通知 |
| `GET` | `/api/v1/guilds/{guild_id}/members` | Guild Memberを参加順で取得する |
| `GET, PATCH, DELETE` | `/api/v1/guilds/{guild_id}/members/{user_id}` | Memberの取得、Nickname変更、削除 |
| `DELETE` | `/api/v1/guilds/{guild_id}/members/@me` | Guildから退出する |
| `GET, POST` | `/api/v1/guilds/{guild_id}/invites` | 有効なInviteの一覧取得と作成 |
| `DELETE` | `/api/v1/guilds/{guild_id}/invites/{invite_id}` | Inviteを無効化する |
| `GET, POST` | `/api/v1/guilds/{guild_id}/roles` | Roleの一覧取得と作成 |
| `PATCH, DELETE` | `/api/v1/guilds/{guild_id}/roles/{role_id}` | Roleの変更と削除 |
| `PUT` | `/api/v1/users/@me/presence` | 自分のPresenceを更新する |
| `GET` | `/api/v1/invites/{invite_code}` | Inviteの参加先を確認する |
| `POST` | `/api/v1/invites/{invite_code}/accept` | Inviteを使用してGuildへ参加する |
| `GET` | `/gateway/v1` | WebSocket GatewayへUpgradeする |

一覧APIは不透明なCursorと`limit`を使用します。
Guildは参加順、Channelは`position`順、Messageは新しい順で安定してPageを返します。

Message投稿時に`reply_to_message_id`を指定すると、同じText Channel内のMessageへ返信できます。
Serverは返信元の表示用情報をResponseとGateway Eventへ含めます。
返信元を削除した後もIDを保持し、表示用情報を`null`にするため、Clientは返信元を表示できない状態を判別できます。

ReactionはMessage、User、Unicode絵文字の組を一意に保存します。
同じ追加または解除を繰り返しても件数は変化せず、APIは操作後の件数と認証済みUser自身の状態を返します。

## InviteとMember

Inviteは推測できないCodeを持ち、任意で有効期限（300秒〜7日）と利用回数（1〜1000）を設定できます。
Inviteを使用できなくなる条件は次のとおりです。

- 無効化した場合は、Codeが存在しないときと同じ`404 NOT_FOUND`を返します。
- 期限切れまたは利用回数を使い切った場合は、参加を`409 INVITE_UNAVAILABLE`で拒否し、参加前の確認は`404 NOT_FOUND`を返します。

参加済みのUserが同じInviteを再送した場合は、利用回数を消費せず既存のMemberを返します。
このため、通信断後の再試行や、上限に達した直後の再送でも結果が変わりません。

MemberのNicknameは本人か、`MANAGE_MEMBERS`を持つ上位のMemberが変更できます。空文字列とnullは設定の解除として扱います。
`role_ids`は割り当てるRoleの全体を置き換えます。既定Roleや存在しないRoleを指定すると`400 INVALID_REQUEST`です。
Presenceは利用者が`PUT /users/@me/presence`で公開する短命な状態です。Process Memoryだけに保持するため、未設定のUserと再起動後は`OFFLINE`を返します。

## RoleとPermission

各Guildには、全Memberへ適用される管理対象の既定Role（`@everyone`、position 0）が作られます。
既定Roleの権限は`VIEW_CHANNEL`、`SEND_MESSAGES`、`CONNECT`、`SPEAK`、`STREAM`です。
Memberの権限は、既定Roleと割り当てられたRoleの権限の論理和です。`role_ids`には既定Roleを含めません。
Guild Ownerは常に全権限を持ち、Roleの階層に縛られません。

| 操作 | 必要な権限 |
| --- | --- |
| Guildの変更、Inviteの一覧・無効化 | `MANAGE_GUILD` |
| Inviteの作成 | `CREATE_INVITE` |
| Channelの作成・変更・削除 | `MANAGE_CHANNELS` |
| Messageの投稿 | `SEND_MESSAGES` |
| 他人のMessageの削除 | `MANAGE_MESSAGES` |
| Memberの削除、他人のNickname変更 | `MANAGE_MEMBERS` |
| Roleの作成・変更・削除、Roleの割り当て | `MANAGE_ROLES` |

RoleはpositionがOwner以外の操作者の最上位Roleより小さい場合だけ管理できます。
Owner以外が作成したRoleはposition 1から始まります。
操作者が持たない権限は、Roleへ付与も剥奪もできません。
対象Memberが操作者以上のRoleを持つ場合と、Ownerに対する操作は`403 FORBIDDEN`です。
Guildの削除はOwner専用です。

その他の規則は次のとおりです。

- Guild MemberだけがGuild、Channel、Messageを参照できる（Channel単位の権限上書きは未実装）
- Guild OwnerはMemberとして削除できず、退出もできない（Guildの削除が必要）
- MemberのNicknameは本人が変更できる
- MessageのAuthorだけが本文を編集できる
- MessageのAuthorは自分のMessageを削除できる

存在しないResourceと、認証済みUserから参照できないResourceは、どちらも`404 NOT_FOUND`として返します。
これにより、参加していないGuildやChannelの存在をAPIから推測できないようにします。

## WebSocket Gateway

Clientは`/gateway/v1`へ接続すると`HELLO`を受信し、Access TokenとIntentを含む`IDENTIFY`を送信します。
認証に成功すると、ServerはGateway Session IDとResume URLを含む`READY`を返します。

`GUILD_MESSAGES` Intentを購読したGuild MemberのSessionには、REST APIで確定した変更を次のEventとして配信します。

- `MESSAGE_CREATE`
- `MESSAGE_UPDATE`
- `MESSAGE_DELETE`

`REACTIONS` Intentを購読したSessionには、操作後の件数を含む次のEventを配信します。

- `MESSAGE_REACTION_ADD`
- `MESSAGE_REACTION_REMOVE`

`GUILD_MEMBERS` Intentを購読したGuild MemberのSessionには、次のEventを配信します。

- `MEMBER_JOIN`
- `MEMBER_UPDATE`
- `MEMBER_LEAVE`

`MEMBER_LEAVE`は、削除または退出したUser本人のSessionにも配信します。
本人は既にGuildの一覧から外れているため、Clientは受信したEventで表示中のGuildを閉じられます。

`GUILD_PRESENCES` Intentを購読したSessionには、Presenceを更新したUserが参加するGuildごとに`PRESENCE_UPDATE`を配信します。

`TYPING` Intentを購読したSessionには、Userの公開情報と通知時刻を含む`TYPING_START`を配信します。
入力中通知はDatabaseへ保存せず、Clientが10秒で失効させます。

`MESSAGE_CONTENT` IntentがないSessionでは、作成・更新Eventに含まれるMessage本文と返信元本文を`null`にします。
投稿元のSessionも配信対象に含まれるため、ClientはMessage IDでREST ResponseとEventを重複排除します。

Dispatch EventのSequenceと直近EventはProcess Memoryへ保持します。
一時切断後は`RESUME`で最後に処理したSequenceを送り、保持期間とBufferの範囲内なら未処理Eventを再配信します。
HeartbeatごとにAccess Tokenを再検証し、期限切れまたはRotation済みTokenの接続を終了します。

## 認証データの境界

**Identity** は User の本人確認方法です。
初期実装は `PASSWORD` を使用し、将来の Google OpenID Connect は `GOOGLE` Identity として同じ User へ Link します。
外部 Identity は Email Address だけでなく、Provider と Provider Subject の組で識別します。

**Session** は本人確認後に Aster Server が発行する認可情報です。
Aster API は短時間有効な不透明 Access Token を受け付け、Google の Token を受け付けません。
Database には Token の SHA-256 Hash だけを保存します。

Refresh Token は使用するたびに交換します。
使用済み Refresh Token が再利用された場合、Server は同じ Session を失効させます。
認証の成功と拒否は、Password や Token を含めず、構造化 Audit Log として標準出力へ記録します。

Password は Argon2id の PHC 形式で保存します。
既定値は OWASP の最低推奨値に対応する Memory 19 MiB、Iteration 2、Parallelism 1 です。

## ローカル起動

Docker Compose を使う場合は、PostgreSQL、Migration、Server をまとめて起動できます。

```bash
make docker-up
```

起動後に Health Check を確認します。

```bash
curl http://localhost:8080/api/v1/health
```

終了時は Container を停止します。
Database Volume は残るため、次回の起動でもデータを引き継ぎます。

```bash
make docker-down
```

Go Process を直接起動する場合は、`.env.example` に記載した環境変数を設定して `make run` を実行します。
`ASTER_AUTO_MIGRATE` の既定値は `false` です。

## 設定

| 環境変数 | 既定値 | 説明 |
| --- | --- | --- |
| `ASTER_HTTP_ADDR` | `:8080` | HTTP Listen Address |
| `ASTER_DATABASE_URL` | なし | PostgreSQL Connection URL。必須 |
| `ASTER_ACCESS_TOKEN_TTL` | `15m` | Access Token の有効期間 |
| `ASTER_REFRESH_TOKEN_TTL` | `720h` | Refresh Token と Session の有効期間 |
| `ASTER_SHUTDOWN_TIMEOUT` | `10s` | Graceful Shutdown の待機時間 |
| `ASTER_GATEWAY_URL` | `ws://localhost:8080/gateway/v1` | `READY`でClientへ返す公開Gateway URL |
| `ASTER_GATEWAY_HEARTBEAT_INTERVAL` | `45s` | ClientがHeartbeatを送る間隔 |
| `ASTER_GATEWAY_IDENTIFY_TIMEOUT` | `10s` | 接続後に`IDENTIFY`または`RESUME`を待つ時間 |
| `ASTER_GATEWAY_SESSION_RETENTION` | `2m` | 切断したGateway SessionとEventを保持する時間 |
| `ASTER_GATEWAY_ALLOWED_ORIGINS` | Local Vite/Tauri Origins | Cross-Origin WebSocketを許可するOriginのComma区切り一覧 |
| `ASTER_AUTO_MIGRATE` | `false` | 起動時に未適用 Migration を実行するか |

## 検証

単体テスト、Race Detector、静的検査を実行します。

```bash
make check
```

PostgreSQL を使用する認証・Chat Lifecycle Test は、`ASTER_TEST_DATABASE_URL` を設定して実行します。

```bash
make test-integration
```

## 現在の制約

- Rate Limit は Process Memory に保存するため、複数 Instance 間では共有しません。
- Email Verification、Password Reset、Account Link、Google OIDC は未実装です。
- Channel単位の権限上書き、Message検索、Channel既読位置は未実装です。
- Category、DM、Thread、添付ファイル、Voice Channelのjoin・state APIとGateway通知は未実装です。
- Gateway SessionとEvent BufferはProcess Memoryにあるため、別InstanceへのResumeとInstance間配信には未対応です。
- Access Token は現在の Session ごとに一つだけ有効であり、Refresh 時に直前の Access Token を失効させます。
- Migration の自動実行は単一の PostgreSQL Advisory Lock で直列化します。

## ライセンス

Aster Server は [Apache License 2.0](LICENSE) の下で公開されています。

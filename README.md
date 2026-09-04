# Aster Server

Aster Server は、Aster の REST API、WebSocket Gateway、永続データを管理する Go Backend です。
現在はPassword認証、Aster Session、Guild、Channel、Message、返信、Reaction、Member、Presence、Role、Permission、Invite、Category、DM、Thread、検索、既読位置の永続化とWebSocket配信を提供します。

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
| `GET, POST` | `/api/v1/users/@me/channels` | DM Channelの一覧取得と作成 |
| `GET, POST` | `/api/v1/channels/{channel_id}/threads` | Threadの一覧取得と作成 |
| `GET` | `/api/v1/guilds/{guild_id}/messages/search` | Guild内Messageの検索 |
| `GET` | `/api/v1/users/@me/read-states` | 自分の既読位置一覧を取得 |
| `PUT` | `/api/v1/channels/{channel_id}/read-state` | Channelの既読位置を更新 |
| `GET, PATCH, DELETE` | `/api/v1/guilds/{guild_id}/members/{user_id}` | Memberの取得、変更、削除 |
| `GET` | `/api/v1/guilds/{guild_id}/members` | Member一覧を取得 |
| `DELETE` | `/api/v1/guilds/{guild_id}/members/@me` | Guildから退出 |
| `GET, POST` | `/api/v1/guilds/{guild_id}/roles` | Roleの一覧取得と作成 |
| `PATCH, DELETE` | `/api/v1/guilds/{guild_id}/roles/{role_id}` | Roleの変更と削除 |
| `GET, POST` | `/api/v1/guilds/{guild_id}/invites` | Inviteの一覧取得と作成 |
| `GET` | `/api/v1/invites/{invite_code}` | Inviteの参加先を確認 |
| `POST` | `/api/v1/invites/{invite_code}/accept` | Inviteを使用して参加 |
| `PUT` | `/api/v1/users/@me/presence` | Presenceを更新 |
| `GET` | `/gateway/v1` | WebSocket GatewayへUpgradeする |

一覧APIは不透明なCursorと`limit`を使用します。
Guildは参加順、Channelは`position`順、Messageは新しい順で安定してPageを返します。

Message投稿時に`reply_to_message_id`を指定すると、同じText Channel内のMessageへ返信できます。
Serverは返信元の表示用情報をResponseとGateway Eventへ含めます。
返信元を削除した後もIDを保持し、表示用情報を`null`にするため、Clientは返信元を表示できない状態を判別できます。

ReactionはMessage、User、Unicode絵文字の組を一意に保存します。
同じ追加または解除を繰り返しても件数は変化せず、APIは操作後の件数と認証済みUser自身の状態を返します。

## Chatの権限

Guild内の操作はRoleに設定したPermission bitで判定します。

- Guild作成者をOwnerかつ最初のMemberにする
- Guild MemberだけがGuild、Channel、Messageを参照できる
- `MANAGE_GUILD`または`MANAGE_CHANNELS`を持つMemberが対象Resourceを管理できる
- `VIEW_CHANNEL`と`SEND_MESSAGES`を持つMemberがGuildのText ChannelとThreadを利用できる
- MessageのAuthorだけが本文を編集できる
- MessageのAuthorまたは`MANAGE_MESSAGES`を持つMemberがMessageを削除できる
- DMは参加者だけが参照・投稿でき、他UserのMessageは削除できない

存在しないResourceと、認証済みUserから参照できないResourceは、どちらも`404 NOT_FOUND`として返します。
これにより、参加していないGuildやChannelの存在をAPIから推測できないようにします。

## WebSocket Gateway

Clientは`/gateway/v1`へ接続すると`HELLO`を受信し、Access TokenとIntentを含む`IDENTIFY`を送信します。
認証に成功すると、ServerはGateway Session IDとResume URLを含む`READY`を返します。

`GUILD_MESSAGES` Intentを購読したGuild MemberのSessionには、REST APIで確定した変更を次のEventとして配信します。

- `MESSAGE_CREATE`
- `MESSAGE_UPDATE`
- `MESSAGE_DELETE`

DMのMessage Eventは`DIRECT_MESSAGES` Intentへ配信します。
GuildのChannel変更は`GUILDS` Intentへ、DM Channel変更は`DIRECT_MESSAGES` Intentへ`CHANNEL_CREATE`、`CHANNEL_UPDATE`、`CHANNEL_DELETE`として配信します。
既読位置の変更は、更新したUser自身の全Gateway Sessionへ`READ_STATE_UPDATE`として配信します。

`REACTIONS` Intentを購読したSessionには、操作後の件数を含む次のEventを配信します。

- `MESSAGE_REACTION_ADD`
- `MESSAGE_REACTION_REMOVE`

`TYPING` Intentを購読したSessionには、Userの公開情報と通知時刻を含む`TYPING_START`を配信します。
入力中通知はDatabaseへ保存せず、Clientが10秒で失効させます。

`GUILD_MEMBERS` IntentにはMemberの参加、変更、退出を、`GUILD_PRESENCES` Intentには短命なPresence更新を配信します。
PresenceはProcess Memoryに保存し、永続プロフィールとは分離します。

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
- 添付ファイル、音声接続、配信のAPIとGateway通知は未実装です。
- Gateway SessionとEvent BufferはProcess Memoryにあるため、別InstanceへのResumeとInstance間配信には未対応です。
- Access Token は現在の Session ごとに一つだけ有効であり、Refresh 時に直前の Access Token を失効させます。
- Migration の自動実行は単一の PostgreSQL Advisory Lock で直列化します。

## ライセンス

Aster Server は [Apache License 2.0](LICENSE) の下で公開されています。

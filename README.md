# Aster Server

Aster Server は、Aster の REST API、WebSocket Gateway、永続データを管理する Go Backend です。
現在は Password 認証と Aster Session の最小実装を提供します。

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
| `ASTER_AUTO_MIGRATE` | `false` | 起動時に未適用 Migration を実行するか |

## 検証

単体テスト、Race Detector、静的検査を実行します。

```bash
make check
```

PostgreSQL を使用する認証 Lifecycle Test は、`ASTER_TEST_DATABASE_URL` を設定して実行します。

```bash
make test-integration
```

## 現在の制約

- Rate Limit は Process Memory に保存するため、複数 Instance 間では共有しません。
- Email Verification、Password Reset、Account Link、Google OIDC は未実装です。
- Access Token は現在の Session ごとに一つだけ有効であり、Refresh 時に直前の Access Token を失効させます。
- Migration の自動実行は単一の PostgreSQL Advisory Lock で直列化します。

## ライセンス

Aster Server は [Apache License 2.0](LICENSE) の下で公開されています。

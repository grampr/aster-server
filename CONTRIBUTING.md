# Aster Server へのコントリビューション

## 開発前の確認

Go 1.26.6 以降と PostgreSQL 17 を使用します。
REST API の Request と Response を変更する場合は、先に Aster Protocol の OpenAPI を変更してください。

## 変更の検証

変更後は次の Command を実行します。

```bash
make check
```

Database Query、Migration、認証 Lifecycle を変更した場合は、PostgreSQL を起動して Integration Test も実行します。

```bash
make test-integration
```

## セキュリティ上の規則

- Password、Raw Token、Authorization Header を Log に出力しない。
- Token は Database に Raw Value で保存しない。
- 認証 Error は Account の存在を推測できる情報を増やさない。
- 外部 Identity を Email Address だけで既存 Account へ自動 Link しない。
- Randomness、Hash Parameter、Token Lifetime の変更には、変更理由と移行方法を記録する。

## Commit と Pull Request

Commit Message は Conventional Commits を使用します。

```text
feat: add email verification
fix: revoke refresh token family on replay
docs: explain session storage
```

Protocol Version、Migration、運用設定への影響を Pull Request に記載してください。

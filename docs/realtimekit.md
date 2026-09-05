# RealtimeKit運用・E2E手順

Aster ServerはCloudflare RealtimeKitのControl Planeだけを担当します。認証済みユーザーのAster Permissionを確認し、MeetingとParticipantを作成して、短命なParticipant TokenをClientへ返します。Cloudflare API TokenをClientへ返したり、ログへ出力したりしません。

## Cloudflare側の準備

検証用と本番用で別々のRealtimeKit Appを作成します。API Tokenには、対象AccountでMeetingとParticipantを作成・削除できるRealtime権限を付与します。権限は必要なAccountへ限定し、TokenはServerのSecretとして保存します。

Appには次のPresetを作成します。

| Preset | Aster Permission | RealtimeKitで許可する操作 |
| --- | --- | --- |
| `group_call_listener` | `CONNECT` | Meeting参加、音声・映像の受信 |
| `group_call_participant` | `CONNECT`、`SPEAK` | Listenerの操作、MicrophoneとCameraの送信 |
| `group_call_host` | `CONNECT`、`SPEAK`、`STREAM` | Participantの操作、Screen Shareの送信 |

Preset名は大文字と小文字を区別します。別名を使う場合は、Serverの`ASTER_CLOUDFLARE_REALTIME_*_PRESET`も同じ値に変更します。

## Server設定

```dotenv
ASTER_VOICE_PROVIDER=cloudflare-realtimekit
ASTER_CLOUDFLARE_ACCOUNT_ID=<account-id>
ASTER_CLOUDFLARE_REALTIME_APP_ID=<app-id>
ASTER_CLOUDFLARE_API_TOKEN=<server-only-api-token>
ASTER_CLOUDFLARE_REALTIME_LISTENER_PRESET=group_call_listener
ASTER_CLOUDFLARE_REALTIME_VOICE_PRESET=group_call_participant
ASTER_CLOUDFLARE_REALTIME_STREAM_PRESET=group_call_host
```

3つのCloudflare識別情報は一部だけ設定できません。`ASTER_VOICE_PROVIDER=cloudflare-realtimekit`を明示した場合も、すべて必須です。Server起動時に不足を検出すると起動を中止します。

## PermissionとPresetの対応

Voice Channel参加時にAster Serverは`CONNECT`を確認します。`SPEAK`があるユーザーにはVoice Preset、`STREAM`があるユーザーにはStream Presetを選択します。`STREAM`が最も強いPresetとして優先されます。

参加後にScreen Shareを開始する際も`STREAM`を再確認します。Clientの表示状態だけを変更してPermissionを回避することはできません。

## 2クライアントE2E

1. 同じGuildの検証用ユーザーを2人用意し、同じVoice Channelへ`CONNECT`を付与します。
2. 送信側には`SPEAK`と`STREAM`、受信側には必要な範囲のPermissionを付与します。
3. ServerをRealtimeKit設定で起動し、両Clientを同じVoice Channelへ参加させます。
4. 両方のJoin APIが同じMeetingに対する別々のParticipant Tokenを返すことを確認します。Token値そのものは記録しません。
5. Mute解除、Camera開始、Screen Share開始を順に行い、受信側で音声とSurfaceを確認します。
6. 送信側を退出させ、Participant削除、Voice State更新、受信側Surfaceの消去を確認します。

## 障害の切り分け

- Server起動に失敗する場合は、Account ID、App ID、API Tokenの3値とProvider名を確認します。
- Participant作成が`4xx`になる場合は、API TokenのRealtime権限とPreset名を確認します。
- Aster APIが`403`を返す場合は、Guild Roleの`CONNECT`、`SPEAK`、`STREAM`を確認します。
- Participantは参加するがMediaが届かない場合は、ClientのOS権限、RealtimeKit Session、WebRTCを許可するネットワークを確認します。

Cloudflare APIのエラー本文には機密情報や運用情報が含まれる可能性があるため、Aster ServerはProviderのHTTP Statusだけを上位へ返します。

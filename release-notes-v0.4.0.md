# goip-bridge v0.4.0

Шлюз SMS/USSD для GoIP DBL / Hybertone: HTTP API, webhook и опциональная MySQL/MariaDB-очередь inbox/outbox.

## Главное в этом релизе

- **Webhook-мониторинг линий**: `line_down`/`line_up` при пропадании и восстановлении keepalive, `line_failing`/`line_recovered` при серии ошибок отправки (порог - новая настройка `fail_threshold`).
- **Полный поток событий в любом режиме**: если задан `webhook_url`, события `queued`/`sent`/`done`/`failed` шлются и с MySQL-очередью, и в синхронном режиме без базы. `queued` теперь приходит и для строк, добавленных в `goip_outbox` напрямую.
- **Webhook больше не следует за редиректами**: ответ `3xx` - ошибка доставки с понятным `webhook WARN 301 ... Location: ...` в логе (раньше тело POST молча терялось). Статус каждой доставки виден в логе.
- **Самообновление**: `goip-bridge -update` скачивает последний релиз, сверяет SHA256 и атомарно подменяет бинарник (откат через `.bak` при сбое).
- **Проверка новой версии при старте**: настройка `check_updates`, по умолчанию выключена - bridge никуда не «звонит».
- **Чистая папка логов**: при старте логи прошлого запуска переезжают в одну копию `.prev` каждая (`clear_logs_on_start`, по умолчанию включено).
- **Единая «шапка»**: `-version` и стартовый баннер оформлены одной рамкой.

Полный список: `CHANGELOG.md`, секция `[0.4.0]`.

## Скачать

Обычному пользователю нужны готовые файлы из GitHub Releases -> Assets:

- `goip-bridge`
- `goip-bridge-linux-amd64.tar.gz`
- `checksums.txt`

Не скачивайте `Source code`, если хотите просто запустить программу.

## Быстрый старт

```sh
tar -xzf goip-bridge-linux-amd64.tar.gz
cd goip-bridge-linux-amd64
./goip-bridge -config config.json -init ru
./goip-bridge -config config.json
```

## Обновление с прошлой версии

`config.json` обратно совместим: новые поля берут значения по умолчанию. Подмените бинарник и перезапустите сервис - или используйте новое `goip-bridge -update` (см. README, раздел «Обновление версии»).

---

## English

GoIP SMS/USSD gateway for GoIP DBL / Hybertone: HTTP API, webhook and optional MySQL/MariaDB inbox/outbox queue.

### Highlights

- **Line-monitoring webhook events**: `line_down`/`line_up` when keepalive disappears and recovers, `line_failing`/`line_recovered` on a streak of send failures (threshold - the new `fail_threshold` setting).
- **Full event stream in every mode**: with `webhook_url` set, `queued`/`sent`/`done`/`failed` fire both with the MySQL queue and in the synchronous no-DB mode. `queued` now also fires for rows inserted into `goip_outbox` directly.
- **Webhook no longer follows redirects**: a `3xx` answer is a delivery failure with a clear `webhook WARN 301 ... Location: ...` log line (previously the POST body was silently dropped). Every delivery's HTTP status is logged.
- **Self-update**: `goip-bridge -update` downloads the latest release, verifies the SHA256 and swaps the binary atomically (rollback via `.bak` on failure).
- **Startup version check**: the `check_updates` setting, off by default - the bridge never "phones home".
- **Clean log folder**: on startup the previous run's logs move to one `.prev` copy each (`clear_logs_on_start`, on by default).
- **Single boxed header** for `-version` and the startup banner.

Full list: `CHANGELOG.md`, section `[0.4.0]` (English part).

### Updating

`config.json` is backward compatible: new fields fall back to defaults. Swap the binary and restart the service - or use the new `goip-bridge -update` (see README, "Updating the version").

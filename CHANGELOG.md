# Changelog

Формат близок к [Keep a Changelog](https://keepachangelog.com/ru/1.0.0/), версии - SemVer.

## [Unreleased]

### Запланировано

- Webhook-события мониторинга линий: `line_down` (keepalive не приходил дольше `line_dead_after_sec`), `line_up` (линия восстановилась), `line_failing` (несколько ошибок отправки подряд на одной линии, порог настраивается).
- Webhook-событие `queued` для строк, добавленных в `goip_outbox` напрямую из приложения (сейчас `queued` шлётся только при постановке через HTTP `/sms` и `/ussd`).
- Если задан `webhook_url`, события отправки (`sent`, `done`, `failed`) шлются в любом режиме, включая синхронный без MySQL (сейчас - только в MySQL-режиме).
- Доставка webhook не следует за редиректами и логирует HTTP-статус ответа: `2xx` - успех, `3xx` - явное предупреждение, что `webhook_url` редиректит и тело `POST` теряется (сейчас bridge молча идёт по редиректу, теряет тело и считает доставку успешной).

## [0.3.2] - 2026-06-11

### Добавлено

- Модульные тесты (`main_test.go`): разбор протокольных пакетов, дедупликация входящих, загрузка конфига и HTTP-обработчики.

### Исправлено

- Входящие SMS и delivery report во время переподключения к MySQL теперь попадают в fallback-журнал, а не теряются (раньше в окно reconnect они не писались ни в базу, ни в журнал).
- Дедупликация входящих `RECEIVE`/`DELIVER` очищается по времени (~10 минут) независимо от нагрузки — устранён риск ложно отбросить новое сообщение после перезагрузки устройства с повтором номера пакета, а карта ключей больше не растёт без предела при всплеске трафика.
- Логи: после неудачной ротации (нет места или прав) записи больше не уходят молча в уже закрытый файл.
- Прямая отправка без MySQL (`/sms`, `/ussd`) сериализуется по линии — два параллельных запроса на одну SIM больше не накладываются (если линия занята, возвращается `409`).
- Слишком длинный текст SMS или код USSD отклоняется с `400`, а не блокирует линию до истечения таймаута.
- USSD-задания, прерванные аварийным завершением, больше не помечаются ложным временем отправки.
- `/health` сообщает реальное состояние MySQL (быстрый ping), а не просто наличие настройки.
- Некорректные (отрицательные) тайминги в конфиге заменяются значениями по умолчанию вместо аварийного завершения процесса.
- Ошибка привязки HTTP-порта обрабатывается штатно, без обхода корректного завершения.
- Сбой или паника при доставке webhook больше не могут уронить сервис или заблокировать очередь доставки.

### Безопасность

- Пароль линии очищается от управляющих символов (CR/LF) перед отправкой на устройство — выученный из keepalive пароль нельзя использовать для вставки лишней протокольной строки.
- Подключение к MySQL собирается средствами драйвера (корректное экранирование спецсимволов в пароле) и получает таймауты, чтобы зависшая база не блокировала обработку.
- HTTP-сервер получил таймауты чтения и простоя соединения (защита от медленных клиентов); запись ответа рассчитана так, чтобы не обрывать синхронную отправку.
- `RECEIVE` без идентификатора линии отбрасывается; `sms_no` в delivery report проверяется как число перед сопоставлением со строкой в базе.
- Предупреждение при старте, если приём UDP открыт (пустые `allow_src` и `line_passwords`) либо `http_token` пуст, слишком короткий или остался placeholder при не-loopback адресе.

### Документация

- Добавлен `CONFIG.md` - полный справочник по настройкам `config.json`, CLI-флагам, дефолтам, webhook retry, pacing, логам и MySQL-режиму.
- Переписан `API.md` по фактическим handler-функциям: асинхронные `/sms` и `/ussd` при MySQL, `GET /status/{id}`, `DELETE /message/{id}`, webhook-события, retry и коды ошибок.
- Возвращён минимальный `config.no-mysql.example.json` в каталог основного проекта.
- Добавлен `LICENSE` с MIT License.
- Подготовлены release artifacts: `goip-bridge-linux-amd64.tar.gz` и `checksums.txt` с SHA256 для бинарника и архива.
- README, INSTALL, MYSQL, TROUBLESHOOTING, DOWNLOAD и SCHEMES синхронизированы с реальным поведением v0.3.1: `webhook_retry`, `send_pacing`, `default_lines`, `done`, `cancelled`, fallback-журнал и фактические сообщения MySQL reconnect.

## [0.3.1] - 2026-06-10

### Документация

- Ссылки на скачивание бинарника переведены на постоянный пермалинк `releases/latest/download/goip-bridge` - он всегда ведёт на последний релиз, ссылки больше не нужно править под каждую версию.
- Устранён разнобой версий в документации (часть страниц ссылалась на старый релиз).

## [0.3.0] - 2026-06-10

### Добавлено

- Баннер версии первой строкой лога при старте и флаг `-version` - сразу видно, какая версия запущена.
- `GET /health` - лёгкий эндпоинт мониторинга без токена: число линий, живых линий, статус MySQL.
- Режим `debug` и файловые логи рядом с конфигом: `goip-bridge.log` и `goip-bridge.err.log` с ротацией по `log_max_mb` (по умолчанию 10 МБ).
- Журнал `goip-bridge.fallback.jsonl`: при недоступности MySQL входящие SMS, статусы отправки и delivery report дописываются в файл, а не теряются молча.
- `allow_src` - список IP/CIDR, с которых принимаются UDP-пакеты от GoIP.
- `line_dead_after_sec` - линия считается не живой, если keepalive не приходил дольше заданного времени.
- Корректное завершение по сигналу: bridge дожидается незавершённых отправок, прежде чем закрыть сокет и базу.
- Повторное подключение к MySQL в фоне каждые 15 секунд после обрыва.
- Восстановление при старте: задания, застрявшие в `sending` после аварийного завершения, возвращаются в очередь.

### Исправлено

- Валидация номера получателя (`+` и 3-20 цифр) - некорректный номер не уходит на устройство искажённым.
- Гонка между регистрацией отправки и остановкой сервиса - исключена возможная паника при завершении.
- Delivery report, для которого не нашлась строка `sent`, больше не теряется молча, а пишется в журнал fallback.

### Безопасность

- Файлы логов и журнал fallback создаются с правами `0600` (внутри номера и тексты SMS).
- Предупреждение в лог, если `http_token` пуст, а HTTP API слушает не на localhost.
- Источник UDP-пакетов можно ограничить через `allow_src`.

### Документация

- Исправлены строки логов в инструкциях: фактическое поведение при недоступности MySQL (фоновый повтор подключения каждые 15 секунд).
- Описаны новые настройки `debug`, `log_max_mb`, `line_dead_after_sec`, `allow_src` и журнал `goip-bridge.fallback.jsonl`.
- Дополнен формат `error_code` и описан механизм восстановления очереди при старте.

## [0.2.0] - 2026-06-09

### Добавлено

- Опциональная MySQL/MariaDB-интеграция через блок `db` в `config.json`.
- Запись входящих SMS в таблицу `goip_inbox`.
- Очередь исходящих SMS из таблицы `goip_outbox`.
- Обновление статусов исходящих сообщений: `sending`, `sent`, `delivered`, `failed`.
- `goip-bridge.service` для запуска через systemd.

### Исправлено

- Распознавание успешной отправки `OK <id> <ref> <sms_no>`.
- Защита от дублей при ожидании `WAIT`.
- USSD-команда отправляется без лишнего перевода строки.
- Поведение при гонке между записью `sent` и приходом DLR.

### Документация

- Обновлены README, INSTALL, DOWNLOAD.
- Добавлены API, MySQL и troubleshooting-инструкции.
- Добавлен минимальный пример конфига без MySQL.
- Уточнено фактическое поведение API: HTTP `200` при `status=failed`, непроверяемые HTTP-методы, выбор линии при пустом `line`.
- Документированы `line_passwords`, webhook timeout 15 секунд, MySQL runtime-лимиты и формат `dlr_state:N`.

### Исправлено в dev tools

- `goip-sim` теперь отвечает `OK <id> <ref> <sms_no>` после `WAIT`, чтобы локальная отправка SMS не уходила в timeout.

## [0.1.0] - 2026-06-09

### Добавлено

- Первый релиз: прием и отправка SMS, USSD, delivery reports.
- HTTP API: `/lines`, `/sms`, `/ussd`, `/inbox`.
- Bearer-токен для HTTP API.
- Исходящий webhook для входящих SMS и DLR.
- Один бинарник Linux amd64.

---

## English

## [Unreleased]

### Planned

- Line-monitoring webhook events: `line_down` (no keepalive for longer than `line_dead_after_sec`), `line_up` (line recovered), `line_failing` (several consecutive send errors on one line, threshold configurable).
- A `queued` webhook event for rows inserted into `goip_outbox` directly by the application (today `queued` fires only when a job is enqueued via the HTTP `/sms` and `/ussd` endpoints).
- When `webhook_url` is set, send events (`sent`, `done`, `failed`) are delivered in every mode, including synchronous no-MySQL mode (today only in MySQL mode).
- Webhook delivery no longer follows redirects and logs the response HTTP status: `2xx` is success, `3xx` is a clear warning that `webhook_url` redirects and the `POST` body is dropped (today the bridge silently follows the redirect, loses the body, and counts the delivery as successful).

## [0.3.2] - 2026-06-11

### Added

- Unit tests (`main_test.go`): protocol packet parsing, inbound de-duplication, config loading and HTTP handlers.

### Fixed

- Inbound SMS and delivery reports during a MySQL reconnect now go to the fallback journal instead of being lost (previously, in the reconnect window, they were written neither to the database nor to the journal).
- Inbound `RECEIVE`/`DELIVER` de-duplication is now purged on a time basis (~10 minutes) regardless of load — this removes the risk of falsely dropping a new message after a device reboot reuses a packet sequence number, and the key map no longer grows unbounded during a traffic burst.
- Logs: after a failed rotation (no disk space or permissions) entries are no longer silently written to an already-closed file.
- Direct send without MySQL (`/sms`, `/ussd`) is serialized per line — two concurrent requests to the same SIM no longer interleave (a busy line returns `409`).
- Over-long SMS text or USSD code is rejected with `400` instead of blocking the line until timeout.
- USSD jobs interrupted by a crash are no longer stamped with a false send time.
- `/health` reports the live MySQL state (a quick ping) rather than just whether it is configured.
- Invalid (negative) timing values in the config fall back to defaults instead of crashing the process.
- An HTTP port-bind error is handled cleanly, without bypassing graceful shutdown.
- A failure or panic during webhook delivery can no longer crash the service or stall the delivery queue.

### Security

- The line password is stripped of control characters (CR/LF) before being sent to the device — a password learned from keepalive can no longer be used to inject an extra protocol line.
- The MySQL connection string is built by the driver (correct escaping of special characters in the password) and gets connection timeouts, so a hung database cannot block processing.
- The HTTP server gained read and idle timeouts (slow-client protection); the response-write timeout is sized so it never cuts off a synchronous send.
- A `RECEIVE` without a line id is dropped; the delivery-report `sms_no` is validated as a number before it is matched against a database row.
- A startup warning is logged when UDP intake is open (empty `allow_src` and `line_passwords`) or when `http_token` is empty, too short, or left as the placeholder on a non-loopback address.

### Documentation

- Added `CONFIG.md`, a full reference for `config.json`, CLI flags, defaults,
  webhook retry, pacing, logs and MySQL mode.
- Rewrote `API.md` against the actual HTTP handlers: asynchronous `/sms` and
  `/ussd` with MySQL, `GET /status/{id}`, `DELETE /message/{id}`, webhook
  events, retry behavior and error codes.
- Restored the minimal `config.no-mysql.example.json` in the main project
  directory.
- Added `LICENSE` with MIT License.
- Prepared release artifacts: `goip-bridge-linux-amd64.tar.gz` and
  `checksums.txt` with SHA256 sums for the binary and archive.
- Synchronized README, INSTALL, MYSQL, TROUBLESHOOTING, DOWNLOAD and SCHEMES
  with real v0.3.1 behavior: `webhook_retry`, `send_pacing`, `default_lines`,
  `done`, `cancelled`, fallback journal and actual MySQL reconnect messages.

## [0.3.1] - 2026-06-10

### Documentation

- Binary download links now use the permanent `releases/latest/download/goip-bridge` permalink, which always points to the newest release, so links no longer need bumping per version.
- Fixed version drift across the docs (some pages still pointed to an older release).

## [0.3.0] - 2026-06-10

### Added

- Version banner as the first log line at startup and a `-version` flag.
- `GET /health` - lightweight, token-free monitoring endpoint (lines, alive lines, MySQL status).
- `debug` mode and file logs next to the config: `goip-bridge.log` and `goip-bridge.err.log`, rotated by `log_max_mb` (default 10 MB).
- `goip-bridge.fallback.jsonl` journal: when MySQL is down, inbound SMS, send statuses and delivery reports are appended to a file instead of being lost.
- `allow_src` - IP/CIDR allow-list for incoming GoIP UDP packets.
- `line_dead_after_sec` - a line is considered dead if no keepalive arrived within the given time.
- Graceful shutdown: the bridge drains in-flight sends before closing the socket and the database.
- Background MySQL reconnect every 15 seconds after a drop.
- Startup reconcile: jobs stuck in `sending` after a crash are returned to the queue.

### Fixed

- Recipient number validation (`+` and 3-20 digits) - a malformed number is no longer sent to the device.
- Race between registering a send and shutting down - removed a possible panic on exit.
- A delivery report with no matching `sent` row is no longer dropped silently; it goes to the fallback journal.

### Security

- Log files and the fallback journal are created with `0600` permissions (they contain numbers and SMS text).
- Warning is logged if `http_token` is empty while the HTTP API listens on a non-loopback address.
- UDP packet source can be restricted via `allow_src`.

## [0.2.0] - 2026-06-09

### Added

- Optional MySQL/MariaDB integration via the `db` config section.
- Inbound SMS insert into `goip_inbox`.
- Outbound SMS queue from `goip_outbox`.
- Delivery status updates: `sending`, `sent`, `delivered`, `failed`.
- systemd unit file.

### Fixed

- Send-success detection for `OK <id> <ref> <sms_no>`.
- Duplicate send behavior while waiting for final device response.
- USSD command trailing newline.
- Race between `sent` updates and DLR processing.

## [0.1.0] - 2026-06-09

### Added

- Initial SMS receive/send, USSD and DLR support.
- HTTP API: `/lines`, `/sms`, `/ussd`, `/inbox`.
- Bearer token authentication.
- Outgoing webhook for inbound SMS and DLR.
- Linux amd64 binary.

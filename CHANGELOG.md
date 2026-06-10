# Changelog

Формат близок к [Keep a Changelog](https://keepachangelog.com/ru/1.0.0/), версии - SemVer.

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
- Уточнено фактическое поведение API: HTTP `200` при `status=failed`, непроверяемые HTTP-методы, выбор линии без гарантии порядка.
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

# Changelog

Формат — [Keep a Changelog](https://keepachangelog.com/ru/1.0.0/), версии — [SemVer](https://semver.org/lang/ru/).

## [0.1.0] — 2026-06-09
### Добавлено
- Первая версия: приём и отправка SMS, USSD, отчёты о доставке (DLR).
- HTTP API (`/lines`, `/sms`, `/ussd`, `/inbox`) с bearer-токеном.
- Исходящий вебхук для входящих SMS и DLR.
- Один статический бинарь (`linux/amd64`), без БД и внешних зависимостей.

---

## [0.1.0] — 2026-06-09 (English)
### Added
- Initial version: SMS receive/send, USSD, delivery reports (DLR).
- HTTP API (`/lines`, `/sms`, `/ussd`, `/inbox`) with bearer token.
- Outgoing webhook for inbound SMS and DLR.
- Single static binary (`linux/amd64`), no DB, no external dependencies.

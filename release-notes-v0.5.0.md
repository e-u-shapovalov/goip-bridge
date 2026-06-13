# goip-bridge v0.5.0

Шлюз SMS/USSD для GoIP DBL / Hybertone: HTTP API, webhook и опциональная MySQL/MariaDB-очередь inbox/outbox.

## Главное в этом релизе

- **Команды управления `status` и `reset`** - и через очередь (`INSERT` в `goip_outbox` с `type='cmd'`), и через HTTP (`POST /stats`, `POST /reset`). Ответ всегда приходит во входящие (`goip_inbox`, `line='system'`) и в webhook одним и тем же телом.
  - `status` - версия, аптайм, память процесса и сервера (`/proc/meminfo`), состояние всех линий и счётчики очереди.
  - `reset` - мягкий сброс без перезапуска: отменяет всю очередь `queued` и чистит кеши в ОЗУ. Нужен, когда в очередь залит не тот список номеров, а доступа к рестарту/руту нет.
- **Описания кодов ошибок**: `errorstatus:N` и `dlr_state:N` теперь сопровождаются текстом (например `errorstatus:38 — Network out of order`) - в колонке `error_code` и отдельным полем `error_desc`/`state_desc` в webhook.
- **Надёжность при остановке и ротации**: фоновые записи в БД (входящие SMS, отчёты о доставке, ответы команд) дренируются при остановке до закрытия БД; устранено падение при ротации лога на заполненном диске; `-update` сохраняет резервную копию до подтверждённого рестарта.
- **Защита от плохих данных**: строки очереди проверяются до захвата линии (битые больше не блокируют отправку), USSD-код валидируется по строгому набору символов, входящие поля из сети ограничены по длине.

Полный список: `CHANGELOG.md`, секция `[0.5.0]`.

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

`config.json` обратно совместим: новые возможности не требуют новых полей. Подмените бинарник и перезапустите сервис - или используйте `goip-bridge -update`. Схема БД не менялась: команды `status`/`reset` используют существующую колонку `type` (`'cmd'`) и пишут ответ в существующую таблицу `goip_inbox`.

---

## English

GoIP SMS/USSD gateway for GoIP DBL / Hybertone: HTTP API, webhook and optional MySQL/MariaDB inbox/outbox queue.

### Highlights

- **Control commands `status` and `reset`** - via the queue (`INSERT` into `goip_outbox` with `type='cmd'`) and via HTTP (`POST /stats`, `POST /reset`). The reply always arrives in the inbox (`goip_inbox`, `line='system'`) and in the webhook with the same body.
  - `status` - version, uptime, process and server memory (`/proc/meminfo`), every line's state and queue counts.
  - `reset` - a soft reset without a restart: cancels the whole `queued` backlog and flushes in-RAM caches. For when a wrong batch was queued and there is no access to a restart/root.
- **Error-code descriptions**: `errorstatus:N` and `dlr_state:N` now carry a text (e.g. `errorstatus:38 — Network out of order`) - in the `error_code` column and as a separate `error_desc`/`state_desc` webhook field.
- **Shutdown and rotation robustness**: background DB writes (inbound SMS, delivery reports, command replies) are drained on shutdown before the DB closes; a crash on log rotation with a full disk is fixed; `-update` keeps the backup until the restart is confirmed.
- **Bad-data hardening**: outbox rows are validated before a line is claimed (bad rows no longer block sending), the USSD code is validated against a strict charset, and inbound network fields are length-bounded.

Full list: `CHANGELOG.md`, section `[0.5.0]` (English part).

### Updating

`config.json` is backward compatible: the new features need no new fields. Swap the binary and restart the service - or use `goip-bridge -update`. The DB schema is unchanged: `status`/`reset` use the existing `type` column (`'cmd'`) and write the reply into the existing `goip_inbox` table.

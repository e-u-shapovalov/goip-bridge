# HTTP API goip-bridge

API нужен вашему приложению: CRM, боту, панели, биллингу, мониторингу или любому backend-сервису.

Базовый адрес в примерах:

```text
http://127.0.0.1:8080
```

Если в `config.json` указан `http_token`, каждый запрос должен содержать:

```text
Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN
```

## Важное поведение

- HTTP-метод проверяется: на неправильный метод эндпоинт отвечает `405` с заголовком `Allow`. `/lines`, `/inbox`, `/health` — только `GET`; `/sms`, `/ussd` — только `POST`.
- Ошибка отправки SMS на уровне GoIP не всегда означает HTTP-ошибку. Если запрос JSON корректный и линия найдена, `/sms` возвращает HTTP `200`, а результат нужно смотреть в поле `status`: `sent` или `failed`.
- Если `line` пустой, bridge выбирает живую линию с наименьшим `id` (детерминированно, без учёта оператора и загрузки). Для предсказуемой маршрутизации указывайте конкретный `line`, например `Go1`.
- Валидация тела: пустой `code` в `/ussd` → `400 need code`; пустые `to`/`text` в `/sms` → `400 need to + text`. Тело запроса ограничено 1 МБ.

## GET /lines

Показывает линии GoIP, которые прислали keepalive.

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Пример ответа:

```json
[
  {
    "id": "Go1",
    "addr": "192.168.1.50:12345",
    "num": "996700000001",
    "signal": 25,
    "gsm_status": "LOGIN",
    "imei": "111111111111111",
    "imsi": "222",
    "iccid": "333",
    "carrier": "Operator",
    "alive": true,
    "last_seen": "2026-06-09T18:00:00Z"
  }
]
```

Главные поля:

- `id` - имя линии, его можно передавать в `/sms` и `/ussd`.
- `alive` - можно ли сейчас использовать линию.
- `gsm_status` - статус GSM-модуля; для рабочей линии обычно `LOGIN`.
- `signal` - уровень сигнала, как его передал GoIP.

## GET /health

Лёгкий эндпоинт для мониторинга. **Не требует токена** (рассчитан на localhost-проверки).

```sh
curl http://127.0.0.1:8080/health
```

Пример ответа:

```json
{
  "ok": true,
  "lines": 8,
  "alive": 8,
  "db": true
}
```

- `lines` - сколько линий зарегистрировано (прислали keepalive).
- `alive` - сколько из них сейчас в строю (`alive: true`).
- `db` - подключён ли MySQL (только если режим БД включён в конфиге).

## POST /sms

Отправляет SMS через GoIP.

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"996700000001","text":"Test message"}'
```

Тело запроса:

```json
{
  "line": "Go1",
  "to": "996700000001",
  "text": "Test message"
}
```

`line` можно оставить пустым:

```json
{
  "line": "",
  "to": "996700000001",
  "text": "Test message"
}
```

Тогда bridge выберет живую линию с наименьшим `id` (детерминированно, без учёта оператора и загрузки). Для production лучше указывать `line` явно.

Успешный ответ:

```json
{
  "line": "Go1",
  "sms_no": "123",
  "status": "sent"
}
```

Ошибка отправки от устройства:

```json
{
  "line": "Go1",
  "status": "failed",
  "error": "timeout"
}
```

Такой ответ приходит с HTTP `200`. Клиент должен проверять `status`, а не только HTTP-код.

Если нет живой линии:

```json
{
  "error": "no alive line"
}
```

## POST /ussd

Выполняет USSD-команду, например запрос баланса.

```sh
curl -X POST http://127.0.0.1:8080/ussd \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","code":"*100#"}'
```

Тело запроса:

```json
{
  "line": "Go1",
  "code": "*100#"
}
```

Успешный ответ:

```json
{
  "line": "Go1",
  "reply": "Balance: 42.00"
}
```

Если оператор или устройство не ответили:

```json
{
  "line": "Go1",
  "error": "ussd timeout"
}
```

Если `code` пустой, bridge отвечает `400 need code` и ничего не отправляет устройству.

## GET /inbox

Возвращает последние входящие SMS, которые bridge держит в памяти.

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/inbox
```

Пример ответа:

```json
[
  {
    "line": "Go1",
    "from": "+996555111222",
    "text": "Incoming message",
    "time": "2026-06-09T18:00:00Z"
  }
]
```

Важно: `/inbox` хранит максимум 500 сообщений и очищается после перезапуска. Для постоянного хранения используйте MySQL-режим: [MYSQL.md](MYSQL.md)

## Webhook

Если в `config.json` указан `webhook_url`, bridge отправляет туда HTTP `POST`.

Конфиг:

```json
{
  "webhook_url": "https://example.com/goip-webhook",
  "webhook_token": "WEBHOOK_SECRET"
}
```

Заголовки:

```text
Content-Type: application/json
Authorization: Bearer WEBHOOK_SECRET
```

Входящая SMS:

```json
{
  "type": "sms",
  "line": "Go1",
  "from": "+996555111222",
  "text": "Message text",
  "time": "2026-06-09T18:00:00Z"
}
```

Номер отправителя берется из GoIP-пакета по первому найденному полю в таком порядке: `srcnum`, `num`, `src`, `sender`.

Delivery report:

```json
{
  "type": "dlr",
  "line": "Go1",
  "sms_no": "123",
  "state": "0",
  "time": "2026-06-09T18:00:00Z"
}
```

В GoIP-протоколе `state: "0"` обычно означает доставку. Другие значения bridge передает как есть.

Webhook отправляется HTTP-клиентом с timeout 15 секунд. Если webhook недоступен или отвечает слишком долго, bridge пишет ошибку в лог и не делает повторную доставку webhook-события. Одновременно в полёте держится не больше 16 webhook-запросов; при перегрузке лишние события отбрасываются с записью в лог (источник истины — MySQL и `/inbox`, а не webhook).

## Коды ошибок HTTP

- `401 unauthorized` - нет bearer-токена или токен неправильный.
- `400 bad json` - тело запроса не является JSON (или больше 1 МБ).
- `400 need to + text` - в `/sms` не переданы `to` или `text`.
- `400 need code` - в `/ussd` не передан `code`.
- `404 no alive line` - указанная линия не жива или нет ни одной живой линии.
- `405 method not allowed` - неверный HTTP-метод для эндпоинта (см. заголовок `Allow`).
- `500 ussd timeout` или `ussd error` - ошибка USSD-сессии.
- `200` со `status: "failed"` в `/sms` - запрос обработан, но устройство не подтвердило отправку. Причина лежит в поле `error`.

Практическое правило для клиента:

```text
HTTP != 2xx        -> transport/API error
/sms status=sent   -> SMS принята GoIP
/sms status=failed -> SMS не отправлена, смотреть error
/ussd reply        -> USSD-ответ получен
/ussd error        -> USSD не выполнен
```

## Безопасность

- Не оставляйте `http_token` пустым на сервере, доступном из сети.
- Не публикуйте API напрямую в интернет без VPN, firewall или reverse proxy.
- Логи могут содержать номера телефонов и текст SMS. Ограничьте доступ к серверу.

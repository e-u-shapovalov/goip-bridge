# goip-bridge

Лёгкий standalone-шлюз для GSM-шлюзов **GoIP** (DBL/Hybertone). Принимает и отправляет SMS и выполняет USSD, общаясь с устройством напрямую по UDP-протоколу «SMS Server». Один статический бинарь + один конфиг. Без базы данных и внешних зависимостей.

*(English version below.)*

## Возможности
- Регистрация линий устройства по keepalive (UDP, по умолчанию `:44444`).
- **Приём SMS** → исходящий вебхук + `GET /inbox`. Длинные (многочастные) сообщения собирает само устройство — приходят одним целым.
- **Отправка SMS** → `POST /sms`. На части длинное режет само устройство.
- **USSD** → `POST /ussd` (например, запрос баланса).
- **Отчёты о доставке (DLR)** → вебхук.
- HTTP API с bearer-токеном; исходящий вебхук.

## Быстрый старт
```sh
cp config.example.json config.json    # отредактируйте
go build -o goip-bridge .
./goip-bridge -config config.json
```
На устройстве GoIP в разделе **SMS** для нужного канала укажите **SMS Server IP/Port** = адрес этого сервиса. Client ID и Password — как на устройстве (бридж подхватит их из keepalive).

## Конфиг (`config.json`)
| поле | назначение |
|---|---|
| `listen_udp` | UDP-адрес, куда регистрируются линии GoIP (`:44444`) |
| `listen_http` | адрес HTTP API (`127.0.0.1:8080`) |
| `http_token` | bearer-токен API (пусто = без авторизации) |
| `webhook_url` | сюда POST-ятся входящие SMS и DLR (пусто = выкл) |
| `webhook_token` | bearer для вебхука |
| `send_timeout_sec` / `ussd_timeout_sec` / `retransmit_sec` | таймауты и ретрансмит UDP |
| `line_passwords` | необязательное переопределение паролей линий |

## HTTP API
| метод | запрос | ответ |
|---|---|---|
| `GET /lines` | — | список линий и их статус |
| `POST /sms` | `{"line":"Go1","to":"996700...","text":"..."}` | `{"status":"sent"}` |
| `POST /ussd` | `{"line":"Go1","code":"*100#"}` | `{"reply":"..."}` |
| `GET /inbox` | — | последние принятые SMS |

Все запросы — с заголовком `Authorization: Bearer <http_token>` (если токен задан).

Исходящий вебхук (POST на `webhook_url`):
- входящая SMS: `{"type":"sms","line":"Go1","from":"+996...","text":"...","time":"..."}`
- доставка: `{"type":"dlr","line":"Go1","sms_no":"...","state":"...","time":"..."}`

## Сборка статического бинаря
```sh
CGO_ENABLED=0 go build -o goip-bridge .
```
Бинарь без зависимостей, работает на любом современном Linux x86-64.

---

## English

`goip-bridge` is a lightweight standalone gateway for **GoIP** GSM gateways (DBL/Hybertone). It sends/receives SMS and runs USSD by speaking the device's UDP "SMS Server" protocol directly. Single static binary + one config file. No database, no external dependencies.

### Features
- Line registration via keepalive (UDP, default `:44444`).
- **Inbound SMS** → outgoing webhook + `GET /inbox` (the device reassembles long/multipart messages).
- **Outbound SMS** → `POST /sms` (the device splits long messages).
- **USSD** → `POST /ussd`.
- **Delivery reports (DLR)** → webhook.
- HTTP API with bearer token; outgoing webhook.

### Quick start
```sh
cp config.example.json config.json    # edit it
go build -o goip-bridge .
./goip-bridge -config config.json
```
On the GoIP device, set the channel's **SMS Server IP/Port** to this service. Client ID/Password stay as on the device — the bridge learns them from keepalive.

### HTTP API
- `GET /lines` — lines and status
- `POST /sms` — `{"line","to","text"}` → `{"status":"sent"}`
- `POST /ussd` — `{"line","code"}` → `{"reply"}`
- `GET /inbox` — recent inbound SMS

Send `Authorization: Bearer <http_token>` when a token is configured. Outgoing webhook (POST to `webhook_url`): inbound `{"type":"sms",...}`, delivery `{"type":"dlr",...}`.

### Build
```sh
CGO_ENABLED=0 go build -o goip-bridge .
```

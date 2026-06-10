# Схемы goip-bridge для быстрого понимания

Этот файл для тех, кто впервые видит GoIP, Linux-сервис, firewall и MySQL-очередь. Сначала посмотрите схемы, потом идите в [INSTALL.md](INSTALL.md).

## 1. Общая картина

```mermaid
flowchart LR
    phone[Телефон с SMS] --> gsm[SIM-карта в GoIP]
    gsm --> goip[GoIP DBL / Hybertone]

    goip -->|UDP 44444 SMS Server| fw[Firewall сервера]
    fw --> bridge[goip-bridge]

    bridge -->|GET /lines| api[HTTP API 127.0.0.1:8080]
    bridge -->|POST /sms| api
    bridge -->|POST /ussd| api
    bridge -->|GET /inbox| api

    api --> app[CRM / bot / backend]
    bridge -->|POST webhook SMS + DLR| webhook[Ваш webhook]
    bridge <-->|optional| mysql[(MySQL / MariaDB)]

    mysql --> inbox[goip_inbox]
    mysql --> outbox[goip_outbox]
```

Что важно запомнить:

- GoIP ходит к bridge по `UDP 44444`.
- Вы или ваше приложение ходите к bridge по HTTP API `8080`.
- MySQL/MariaDB нужен только для табличной очереди.
- Если `/lines` пустой, первым делом проверяйте GoIP SMS Server settings, firewall и маршрут.

## 2. Путь студента: от скачивания до первой SMS

```mermaid
flowchart TD
    start[Старт: есть Linux-сервер и GoIP] --> download[Скачать goip-bridge из Releases -> Assets]
    download --> chmod[chmod +x goip-bridge]
    chmod --> config[Создать config.json]
    config --> run[Запустить ./goip-bridge -config config.json]
    run --> log{В логе есть UDP :44444 и HTTP :8080?}
    log -- нет --> fix1[Проверить путь, права, config.json]
    log -- да --> fw[Открыть UDP 44444 в firewall]
    fw --> goip[В GoIP: SMS Server Enable, IP, Port 44444, Client ID, Password]
    goip --> lines[curl GET /lines]
    lines --> alive{Есть alive=true?}
    alive -- нет --> fix2[Проверить IP, порт, firewall, маршрут, GSM LOGIN]
    alive -- да --> sms[curl POST /sms]
    sms --> done[Первая SMS отправлена]
```

Минимальные команды:

```sh
mkdir -p /opt/goip-bridge
cd /opt/goip-bridge
curl -L -o goip-bridge https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
chmod +x goip-bridge
nano config.json
./goip-bridge -config config.json
```

## 3. Настройка GoIP на странице SMS

```text
GoIP web UI
└── Configurations
    └── SMS
        ├── выбрать канал: CH1 / CH2 / ...
        ├── SMS Server: Enable
        ├── SMS Server IP: IP сервера с goip-bridge
        ├── SMS Server Port: 44444
        ├── SMS Client ID: Go1
        ├── Password: пароль линии
        └── Save Changes
```

Пример скриншота: [docs/screenshots/goip-sms-server-settings.png](docs/screenshots/goip-sms-server-settings.png)

## 4. Входящая SMS

```mermaid
sequenceDiagram
    participant Phone as Телефон
    participant GoIP as GoIP
    participant Bridge as goip-bridge
    participant Inbox as /inbox память
    participant DB as MySQL goip_inbox
    participant Hook as webhook

    Phone->>GoIP: SMS на SIM-карту
    GoIP->>Bridge: UDP RECEIVE на порт 44444
    Bridge-->>GoIP: RECEIVE OK
    Bridge->>Inbox: сохранить последние 500 SMS
    alt db включен
        Bridge->>DB: INSERT line, from_number, text, received_at
    end
    alt webhook_url задан
        Bridge->>Hook: POST type=sms
    end
```

Где потом смотреть:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/inbox
```

```sql
SELECT id, line, from_number, text, received_at
FROM goip_inbox
ORDER BY id DESC
LIMIT 20;
```

## 5. Отправка SMS через HTTP API

```mermaid
sequenceDiagram
    participant App as Ваше приложение
    participant Bridge as goip-bridge
    participant GoIP as GoIP
    participant GSM as GSM-сеть

    App->>Bridge: POST /sms {line,to,text}
    Bridge->>Bridge: выбрать указанную line или одну живую без гарантии порядка
    Bridge->>GoIP: UDP MSG
    GoIP-->>Bridge: PASSWORD
    Bridge->>GoIP: PASSWORD <pass>
    GoIP-->>Bridge: SEND
    Bridge->>GoIP: SEND <ref> <number>
    GoIP-->>Bridge: OK <sms_no>
    Bridge-->>App: {"status":"sent","sms_no":"..."}
    GoIP-->>Bridge: DELIVER state=0
```

Команда:

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"996700000001","text":"Test message"}'
```

## 6. Отправка SMS через MySQL-очередь

```mermaid
flowchart LR
    app[Ваше приложение] -->|INSERT status=queued| outbox[(goip_outbox)]
    bridge[goip-bridge poll_sec] -->|SELECT queued| outbox
    bridge -->|UPDATE sending| outbox
    bridge -->|UDP send| goip[GoIP]
    goip -->|OK sms_no| bridge
    bridge -->|UPDATE sent + sms_no| outbox
    goip -->|DLR state=0| bridge
    bridge -->|UPDATE delivered| outbox
```

Статусы:

```mermaid
stateDiagram-v2
    [*] --> queued: INSERT приложением
    queued --> sending: bridge забрал строку
    sending --> sent: GoIP вернул sms_no
    sent --> delivered: DLR state=0
    sending --> failed: timeout / error
    sent --> failed: DLR state != 0
```

SQL для первой проверки:

```sql
INSERT INTO goip_outbox (line, to_number, text, status)
VALUES ('Go1', '996700000001', 'Test from MySQL queue', 'queued');

SELECT id, line, to_number, status, sms_no, error_code, sent_at, delivered_at
FROM goip_outbox
ORDER BY id DESC
LIMIT 20;
```

## 7. Порты и firewall

```mermaid
flowchart LR
    goip[GoIP] -->|UDP 44444 должен быть открыт| bridge[goip-bridge]
    app[CRM / backend] -->|TCP 8080 только если нужно извне| bridge
    bridge -->|TCP 3306 обычно localhost| db[(MySQL / MariaDB)]
```

Проверки:

```sh
sudo nft list ruleset | grep 44444
sudo ss -lunp | grep 44444
curl -i -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

## 8. Что поднимается после ребута

```mermaid
flowchart TD
    boot[Ребут сервера] --> net[network-online.target]
    net --> addr[IP адреса и маршруты]
    addr --> fw[nftables.service грузит /etc/nftables.conf]
    fw --> db[mariadb.service или mysql.service]
    db --> bridge[goip-bridge.service]
    bridge --> ports[UDP 44444 + HTTP 8080]
    ports --> goip[GoIP снова регистрирует линии]
```

Проверить после перезагрузки:

```sh
ip addr
ip route
sudo systemctl is-enabled nftables
sudo systemctl is-enabled mariadb
sudo systemctl is-enabled goip-bridge
sudo systemctl status goip-bridge
```

## 9. Где искать проблему

```mermaid
flowchart TD
    problem[Не работает] --> api{curl /lines отвечает?}
    api -- unauthorized --> token[Проверить Authorization Bearer и http_token]
    api -- нет ответа --> service[Проверить systemctl status и journalctl]
    api -- [] --> udp[Проверить GoIP SMS Server IP/Port, UDP 44444, firewall, route]
    api -- alive=false --> gsm[Проверить SIM, PIN, GSM signal, LOGIN]
    api -- alive=true --> send{POST /sms работает?}
    send -- no alive line --> line[Проверить line id или отправить line пустым]
    send -- timeout --> network[Проверить UDP, пароль линии, SIM, баланс]
    send -- sent --> ok[Bridge работает]
```

Важно для `/sms`: HTTP `200` не всегда значит, что SMS отправлена. Смотрите JSON-поле `status`. Если там `failed`, причина в поле `error`.

Полная диагностика: [TROUBLESHOOTING.md](TROUBLESHOOTING.md)

## 10. Мини-карта файлов

```text
README.md                  главная страница проекта
DOWNLOAD.md                что скачать на GitHub
INSTALL.md                 установка по шагам
SCHEMES.md                 схемы для понимания
API.md                     HTTP API
MYSQL.md                   база, пользователь, таблицы, очередь
mysql.schema.sql           готовая SQL-схема
FIREWALL.md                firewall, nftables, ufw, маршруты
TROUBLESHOOTING.md         диагностика
goip-bridge.service        systemd unit
config.example.json        пример с MySQL
config.no-mysql.example.json пример без MySQL
```

# Установка, настройка и запуск goip-bridge

Это пошаговая инструкция для новичка, администратора или разработчика. Ее можно проходить сверху вниз и копировать команды в терминал.

## Что понадобится

- Аппаратный GSM-шлюз **GoIP / DBL / Hybertone** с режимом **SMS Server**.
- Linux x86-64 / amd64 сервер, VPS, мини-ПК или виртуальная машина.
- Сеть, где GoIP может достучаться до сервера по UDP-порту `44444`.
- `curl` на сервере для проверки HTTP API.
- Для сборки из исходников: Go **1.24** или новее.
- MySQL или MariaDB нужны только если вы хотите хранить входящие SMS и отправлять исходящие через таблицу-очередь.

## Карта запуска

```mermaid
flowchart TD
    A[Linux-сервер] --> B[Скачать binary из Releases]
    B --> C[Создать config.json]
    C --> D[Запустить bridge]
    D --> E[Открыть UDP 44444]
    E --> F[Настроить GoIP SMS page]
    F --> G[Проверить GET /lines]
    G --> H[Отправить POST /sms]
    H --> I[Поставить systemd]
    I --> J[Проверить после ребута]
```

Все схемы с объяснениями: [SCHEMES.md](SCHEMES.md)

## Вариант 1: запуск без MySQL

Подходит для первого теста, webhook-интеграции и прямой отправки через HTTP API.

### 1. Создайте папку

```sh
sudo mkdir -p /opt/goip-bridge
sudo chown "$USER":"$USER" /opt/goip-bridge
cd /opt/goip-bridge
```

### 2. Скачайте готовый релиз

```sh
curl -L -o goip-bridge https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
chmod +x goip-bridge
```

Не скачивайте `Source code`. Подробно: [DOWNLOAD.md](DOWNLOAD.md)

### 3. Создайте `config.json`

Самый простой способ:

```sh
./goip-bridge -config config.json -init ru
```

Команда создаёт подробный JSONC-конфиг с русскими комментариями и не перезаписывает существующий файл. Если нужны английские комментарии:

```sh
./goip-bridge -config config.json -init en
```

Минимальный вариант можно создать вручную:

```sh
nano config.json
```

Минимальный конфиг:

```json
{
  "listen_udp": ":44444",
  "listen_http": "127.0.0.1:8080",
  "http_token": "CHANGE_ME_TO_LONG_RANDOM_TOKEN",
  "webhook_url": "",
  "webhook_token": "",
  "send_timeout_sec": 45,
  "ussd_timeout_sec": 120,
  "ussd_retransmit_sec": 60,
  "line_passwords": {}
}
```

Поменяйте `CHANGE_ME_TO_LONG_RANDOM_TOKEN`. Не оставляйте стандартный токен на сервере.

Если GoIP не передает пароль линии в keepalive или вы хотите жестко задать пароль, заполните `line_passwords`:

```json
{
  "line_passwords": {
    "Go1": "secret-password"
  }
}
```

Ключ `Go1` должен совпадать с `SMS Client ID` в GoIP и с `id` из `GET /lines`.

Полное объяснение каждой настройки: [CONFIG.md](CONFIG.md)

### 4. Запустите вручную

```sh
./goip-bridge -config config.json
```

Оставьте этот терминал открытым на время первого теста. Логи будут видны прямо в нем.

Ожидаемые строки:

```text
goip-bridge listening on UDP :44444 (GoIP lines register here)
HTTP API on 127.0.0.1:8080
```

## Настройка GoIP

Откройте веб-интерфейс GoIP и найдите настройки SMS для нужной линии.

Пример страницы GoIP:

![GoIP SMS Server settings](docs/screenshots/goip-sms-server-settings.png)

Укажите:

```text
SMS Server: Enable
SMS Server IP: IP-адрес сервера с goip-bridge, например 172.16.172.3
SMS Server Port: 44444
SMS Client ID: например Go1
Password: пароль линии
```

Важно:

- IP должен быть адресом сервера, который виден устройству GoIP.
- Порт `44444` должен быть открыт по UDP.
- Если сервер за NAT, настройте маршрутизацию или проброс UDP.
- После изменения нажмите **Save Changes**.
- Если в интерфейсе есть кнопка `*Auto Config Other lines`, используйте ее только если хотите применить похожие SMS Server настройки к другим линиям.

## Проверка после настройки GoIP

В другом терминале выполните:

```sh
curl -i -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Хороший признак:

```json
[
  {
    "id": "Go1",
    "addr": "192.168.1.50:12345",
    "signal": 25,
    "gsm_status": "LOGIN",
    "alive": true
  }
]
```

Если ответ `[]`, bridge еще не получил keepalive от GoIP. Смотрите [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

Что происходит в этот момент:

```mermaid
sequenceDiagram
    participant GoIP as GoIP
    participant FW as Firewall
    participant Bridge as goip-bridge
    participant API as GET /lines

    GoIP->>FW: UDP keepalive на 44444
    FW->>Bridge: пакет пропущен
    Bridge-->>GoIP: reg status 200
    API->>Bridge: curl /lines
    Bridge-->>API: line alive=true
```

## Проверка отправки SMS

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"996700000001","text":"Test from goip-bridge"}'
```

Успешный ответ обычно выглядит так:

```json
{
  "line": "Go1",
  "sms_no": "123",
  "status": "sent"
}
```

Если не знаете имя линии, сначала посмотрите `GET /lines`. Если `line` оставить пустым, bridge выберет одну из живых линий, но порядок выбора не гарантирован. Для предсказуемой отправки указывайте `line` явно.

## Проверка USSD

```sh
curl -X POST http://127.0.0.1:8080/ussd \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","code":"*100#"}'
```

Оператор и SIM-карта должны поддерживать этот USSD-код.

## Входящие SMS и webhook

Последние входящие SMS в памяти:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/inbox
```

Чтобы отправлять входящие SMS и DLR во внешний сервис, укажите в `config.json`:

```json
{
  "webhook_url": "https://example.com/goip-webhook",
  "webhook_token": "WEBHOOK_SECRET"
}
```

Bridge отправит:

```text
Authorization: Bearer WEBHOOK_SECRET
Content-Type: application/json
```

Пример входящей SMS:

```json
{
  "type": "sms",
  "line": "Go1",
  "from": "+996555111222",
  "text": "Message text",
  "time": "2026-06-09T18:00:00Z"
}
```

Пример DLR:

```json
{
  "type": "dlr",
  "line": "Go1",
  "sms_no": "123",
  "state": "0",
  "time": "2026-06-09T18:00:00Z"
}
```

Кроме `sms` и `dlr` на тот же URL приходят события результатов отправки (`queued`, `sent`, `done`, `failed` - в любом режиме, с MySQL и без) и события мониторинга линий (`line_down`, `line_up`, `line_failing`, `line_recovered`). Полный список с примерами: [API.md](API.md). Важно: `webhook_url` должен отвечать `2xx` без редиректов - редирект bridge не выполняет и считает доставку неуспешной (подробности в [TROUBLESHOOTING.md](TROUBLESHOOTING.md)).

## Вариант 2: запуск с MySQL

MySQL-режим нужен, если ваше приложение хочет:

- читать входящие SMS из таблицы;
- класть исходящие SMS в очередь;
- видеть статусы `queued`, `sending`, `sent`, `delivered`, `done`, `failed`, `cancelled`.

### Быстрый SQL под копипаст

Если MySQL/MariaDB уже установлен, из папки проекта можно применить готовую схему:

```sh
sudo mysql < mysql.schema.sql
```

Эта команда создает:

```text
database:      goip_go
db user:       goip_bridge
inbox table:   goip_inbox
outbox table:  goip_outbox
```

После этого замените пароль `CHANGE_ME_STRONG_DB_PASSWORD` в MySQL и в `config.json`.

### Что внутри таблиц

`goip_inbox`:

- `id` - авто-ID входящего сообщения;
- `line` - линия GoIP, например `Go1`;
- `from_number` - номер отправителя;
- `text` - текст входящей SMS;
- `received_at` - время приема.

`goip_outbox`:

- `id` - авто-ID задания;
- `line` - линия GoIP или `NULL` для любой живой линии;
- `to_number` - номер получателя;
- `text` - текст SMS;
- `type` - `sms` или `ussd`;
- `status` - `queued`, `sending`, `sent`, `delivered`, `done`, `failed`, `cancelled`;
- `sms_no` - номер SMS от GoIP;
- `error_code` - ошибка при `failed`;
- `created_at`, `sent_at`, `delivered_at` - времена этапов.

Полная схема, права пользователя, SQL-команды и примеры очереди: [MYSQL.md](MYSQL.md)

Визуальная схема очереди: [SCHEMES.md#6-отправка-sms-через-mysql-очередь](SCHEMES.md#6-отправка-sms-через-mysql-очередь)

### Добавьте MySQL в `config.json`

Пример `db`:

```json
{
  "db": {
    "host": "127.0.0.1",
    "port": 3306,
    "user": "goip_bridge",
    "password": "CHANGE_ME",
    "name": "goip_go",
    "inbox_table": "goip_inbox",
    "outbox_table": "goip_outbox",
    "poll_sec": 3
  }
}
```

Если в логе появилось `db: configured but NOT connected ... — retrying in background; /sms and /ussd return 503 until connected`, bridge будет повторять подключение каждые 15 секунд в фоне. Пока база недоступна, `/sms` и `/ussd` в режиме очереди возвращают `503`, чтобы не отправить сообщение мимо MySQL. `/lines`, `/health` и приём UDP от GoIP продолжают работать. Исправьте доступ к базе - переподключение и разбор очереди произойдут автоматически, перезапуск не обязателен. Данные, которые не удалось записать после нескольких попыток, сохраняются в `goip-bridge.fallback.jsonl` рядом с конфигом.

Runtime-ограничения текущей версии:

- максимум 8 открытых DB-соединений;
- одно активное SMS/USSD-задание на одну линию;
- задержка между заданиями на линии управляется `send_pacing`;
- bridge читает очередь страницами по 100 строк и может пройти до 20 страниц за poll, чтобы строки для мёртвых линий не блокировали остальные;
- при DLR bridge до 6 раз пытается найти соответствующую строку `sent`, пауза между попытками 1.5 секунды.

### Пример отправки SMS через MySQL

```sql
INSERT INTO goip_outbox (type, line, to_number, text, status)
VALUES ('sms', 'Go1', '996700000001', 'Test from MySQL queue', 'queued');
```

Проверить статус:

```sql
SELECT id, line, to_number, status, sms_no, error_code, sent_at, delivered_at
FROM goip_outbox
ORDER BY id DESC
LIMIT 20;
```

## Установка как systemd-сервис

Когда ручной запуск проверен, установите сервис.

### 1. Создайте системного пользователя

```sh
sudo useradd --system --home /opt/goip-bridge --shell /usr/sbin/nologin goip-bridge
```

Если пользователь уже существует, команда может сообщить об ошибке. Это нормально, продолжайте дальше.

### 2. Положите файлы в `/opt/goip-bridge`

```sh
sudo mkdir -p /opt/goip-bridge
sudo cp goip-bridge config.json /opt/goip-bridge/
sudo chown -R goip-bridge:goip-bridge /opt/goip-bridge
```

### 3. Установите unit-файл

Если `goip-bridge.service` лежит рядом:

```sh
sudo cp goip-bridge.service /etc/systemd/system/goip-bridge.service
```

Если файла нет, создайте его:

```sh
sudo nano /etc/systemd/system/goip-bridge.service
```

Содержимое:

```ini
[Unit]
Description=goip-bridge - GoIP SMS/USSD gateway
After=network-online.target nftables.service mariadb.service mysql.service
Wants=network-online.target

[Service]
Type=simple
User=goip-bridge
Group=goip-bridge
WorkingDirectory=/opt/goip-bridge
ExecStart=/opt/goip-bridge/goip-bridge -config /opt/goip-bridge/config.json
Restart=always
RestartSec=3
NoNewPrivileges=true
PrivateTmp=true
ProtectSystem=full
ProtectHome=true

[Install]
WantedBy=multi-user.target
```

Если у вас рабочая папка не `/opt/goip-bridge`, а, например, `/var/www/goip-bridge`, замените `WorkingDirectory` и `ExecStart` в unit-файле:

```ini
WorkingDirectory=/var/www/goip-bridge
ExecStart=/var/www/goip-bridge/goip-bridge -config /var/www/goip-bridge/config.json
```

### 4. Запустите сервис

```sh
sudo systemctl daemon-reload
sudo systemctl enable --now goip-bridge
sudo systemctl status goip-bridge
```

### 5. Смотрите логи

```sh
sudo journalctl -u goip-bridge -f
```

Последние 100 строк:

```sh
sudo journalctl -u goip-bridge -n 100 --no-pager
```

### Обновить unit-файл из GitHub raw

Такой сценарий полезен, если unit-файл уже был создан вручную и вы хотите заменить его версией из репозитория.

Проверьте путь в скачанном unit-файле перед запуском: по умолчанию в документации используется `/opt/goip-bridge`, а на некоторых серверах проект кладут в `/var/www/goip-bridge`.

```sh
sudo pkill -f 'goip-bridge -config' || true
sudo rm -f /etc/systemd/system/goip-bridge.service
sudo curl -fsSLo /etc/systemd/system/goip-bridge.service https://raw.githubusercontent.com/e-u-shapovalov/goip-bridge/main/goip-bridge.service
sudo systemctl daemon-reload
sudo systemctl enable --now goip-bridge
systemctl --no-pager -l status goip-bridge | head -n 12
```

Успешный признак:

```text
Loaded: loaded (/etc/systemd/system/goip-bridge.service; enabled)
Active: active (running)
Main PID: ... (goip-bridge)
```

## Обновление goip-bridge

При обновлении меняется только бинарник: `config.json`, база и unit-файл остаются на месте. Новые поля конфига берут значения по умолчанию, поэтому старый конфиг продолжает работать.

Встроенное самообновление - одна команда. Bridge скачает последний релиз и `checksums.txt`, сверит SHA256, сохранит текущий бинарник в `.bak`, атомарно подменит файл и удалит `.bak` при успехе (при сбое `.bak` остаётся для отката):

```sh
sudo -u goip-bridge /opt/goip-bridge/goip-bridge -update
sudo systemctl restart goip-bridge
```

Перезапуск сервиса - отдельный root-шаг: системный пользователь `goip-bridge` не имеет права на `systemctl restart`. Если запустить `-update` от root, перезапуск выполнится автоматически.

Ручной способ без `-update`:

```sh
sudo curl -L -o /opt/goip-bridge/goip-bridge.new https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
sudo chmod +x /opt/goip-bridge/goip-bridge.new
sudo chown goip-bridge:goip-bridge /opt/goip-bridge/goip-bridge.new
sudo mv /opt/goip-bridge/goip-bridge.new /opt/goip-bridge/goip-bridge
sudo systemctl restart goip-bridge
```

Проверка после перезапуска - первой в логе идёт рамка с новой версией:

```sh
sudo journalctl -u goip-bridge -n 20 --no-pager
/opt/goip-bridge/goip-bridge -version
```

Чтобы узнавать о новых версиях автоматически, включите в `config.json` `"check_updates": true` - при старте bridge фоново спросит GitHub и напечатает рамку, если вышла новая версия. По умолчанию проверка выключена.

## Firewall

Нужно открыть UDP-порт `44444` для GoIP. Это главный порт устройства к bridge.

```sh
sudo ufw allow 44444/udp
```

Если используете `nftables`, правило должно быть сохранено в `/etc/nftables.conf`, иначе после ребута оно пропадет. Пример для GoIP-сети `172.16.172.0/24`:

```nft
udp dport 44444 ip saddr 172.16.172.0/24 accept
```

Применить и включить автозагрузку:

```sh
sudo nft -f /etc/nftables.conf
sudo systemctl enable --now nftables
sudo nft list ruleset | grep 44444
```

HTTP API по умолчанию слушает только `127.0.0.1:8080`. Это безопаснее. Если API нужен с другой машины, поменяйте:

```json
"listen_http": "0.0.0.0:8080"
```

После этого обязательно:

- задайте сильный `http_token`;
- ограничьте доступ firewall;
- лучше публикуйте API через VPN или reverse proxy.

MySQL/MariaDB порт `3306` обычно не нужно открывать наружу, если база стоит на том же сервере.

Подробная инструкция по `nftables`, `ufw`, `firewalld`, серым IP, маршрутам и проверке после ребута: [FIREWALL.md](FIREWALL.md)

## Сборка из исходников

Обычному пользователю это не нужно. Используйте только если хотите менять код или собирать бинарник сами.

```sh
git clone https://github.com/e-u-shapovalov/goip-bridge.git
cd goip-bridge
go run . -config config.json -init ru   # создаст config.json с комментариями (или -init en)
# отредактируйте config.json, затем запустите:
go run . -config config.json
```

Сборка Linux amd64:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o goip-bridge .
```

## Контрольный чеклист

- Скачан asset `goip-bridge` из Releases, не `Source code`.
- Есть `config.json`.
- Токен в curl совпадает с `http_token`.
- `./goip-bridge -config config.json` стартует без ошибки.
- UDP `44444` доступен от GoIP до сервера.
- Firewall разрешает `UDP 44444`, правило сохранено и поднимается после ребута.
- Если используется `nftables`, `nftables.service` включен.
- В GoIP указан правильный `SMS Server IP` и `SMS Server Port`.
- `GET /lines` показывает хотя бы одну линию с `"alive": true`.
- Отправка SMS через `POST /sms` возвращает `status: sent` или понятную ошибку.

Если что-то не сходится, откройте [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

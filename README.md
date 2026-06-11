# goip-bridge - SMS/USSD API, webhook и MySQL-очередь для GoIP

## Быстрый старт

Команды ниже рассчитаны на Linux x86-64 / amd64. Перейдите в root через `sudo -i` или добавляйте `sudo` к системным командам.

Создайте папку и скачайте последний релиз:

```sh
sudo mkdir -p /opt/goip-bridge
cd /opt/goip-bridge
sudo curl -L -o goip-bridge https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
sudo chmod +x goip-bridge
```

Запустите первый раз:

```sh
sudo ./goip-bridge
```

Выберите язык `ru` или `en`. Bridge сам создаст `config.json` и выйдет.

Так выглядит скачивание и первый запуск: bridge выводит баннер с версией, спрашивает язык, создаёт `config.json` и подсказывает, что заполнить.

![Первый запуск goip-bridge](docs/screenshots/first-run.png)

Заполните конфиг:

```sh
sudo nano config.json
```

Минимально поменяйте `http_token`. Если хотите получать webhook, заполните `webhook_url`. Если фиксируете пароль линии в `line_passwords`, этот же пароль укажите потом в GoIP.

Запустите bridge:

```sh
sudo ./goip-bridge -config config.json
```

При старте bridge печатает баннер с версией и таблицу `config in effect` - какие настройки реально применились. Секреты (`http_token`, `webhook_token`, `webhook_url`) показываются как `set`, а не значением, поэтому лог можно показывать. В конце - строки `listening on UDP :44444` и `HTTP API on 127.0.0.1:8080`.

![Лог старта goip-bridge и таблица config in effect](docs/screenshots/startup-config-in-effect.png)

Откройте настройки SMS в GoIP:

```text
http://goip8/default/en_US/config.html?type=sms
```

Если ваш шлюз открывается не как `goip8`, замените адрес на IP или имя своего GoIP. Впишите:

```text
SMS Server: Enable
SMS Server IP: IP вашего Linux-сервера
SMS Server Port: 44444
SMS Client ID: Go1
Password: пароль для этого канала
```

Порт GoIP по умолчанию - UDP `44444`, пять четверок. Не путайте с `4444`. Сохраните настройки и перезагрузите шлюз.

Пример окна GoIP:

![GoIP SMS Server settings](docs/screenshots/goip-sms-server-settings.png)

Проверьте локально, что линия зарегистрировалась:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Проверьте firewall и поднятые порты. Выполняйте команду своего дистрибутива - у остальных firewall может быть не установлен, и `command not found` это нормально.

Debian (nftables):

```sh
sudo nft list ruleset | grep -E '8080|44444'
```

Ubuntu (ufw):

```sh
sudo ufw status verbose
```

RHEL / CentOS / Fedora / Rocky / AlmaLinux (firewalld):

```sh
sudo firewall-cmd --list-ports
```

Кто реально слушает порты (любой дистрибутив):

```sh
sudo ss -lntup | grep -E ':(8080|44444)\b'
```

Открывать нужно UDP `44444` от GoIP к серверу. TCP `8080` открывайте только если HTTP API нужен с другой машины. Как разрешить порты именно в вашем firewall, смотрите в документации своего firewall/дистрибутива.

Проверьте USSD, например баланс. Замените `*100#` на код своего оператора:

```sh
curl -X POST http://127.0.0.1:8080/ussd \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","code":"*100#"}'
```

Без MySQL ответ придет прямо в curl. В MySQL-режиме результат также придет в webhook событием `type:"done"`, если задан `webhook_url`.

Отправьте первое SMS:

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"+996700000001","text":"Test message"}'
```

Формат номера зависит от оператора SIM-карты. Bridge принимает номер с `+` и без него (`+996700000001` или `996700000001`) - проверка простая: опциональный `+` и 3-20 цифр. Но дальше номер обрабатывает сам GoIP и сеть оператора, а у них требования разные: одни принимают международный формат с `+`, другие без `+`, третьи не принимают свой же код страны и ждут местный формат (например `0700000001`). Если SMS уходит в `failed` или не доходит, попробуйте для этого оператора другой формат номера.

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/inbox
```

`/inbox` показывает последние 500 входящих SMS из памяти текущего процесса, а не из базы. После перезапуска bridge этот список очищается. Для постоянного хранения входящих включите MySQL - тогда они пишутся в таблицу `goip_inbox` и переживают перезапуск.

Поздравляем, минимальный запуск освоен.

### Запуск как systemd-сервис

Короткий пример для Debian/systemd x86_64, например:

```text
Linux modern 6.12.74+deb13+1-amd64 #1 SMP PREEMPT_DYNAMIC Debian 6.12.74-2 (2026-03-08) x86_64 GNU/Linux
```

Unit-файл ожидает бинарник и конфиг в `/opt/goip-bridge`:

```sh
sudo useradd --system --home /opt/goip-bridge --shell /usr/sbin/nologin goip-bridge
sudo chown -R goip-bridge:goip-bridge /opt/goip-bridge
sudo curl -L -o /etc/systemd/system/goip-bridge.service https://raw.githubusercontent.com/e-u-shapovalov/goip-bridge/main/goip-bridge.service
sudo systemctl daemon-reload
sudo systemctl enable --now goip-bridge
sudo systemctl status goip-bridge
sudo journalctl -u goip-bridge -f
```

### Обновление версии

При обновлении меняется только бинарник, `config.json` остаётся на месте. Скачайте новую версию рядом, атомарно подмените и перезапустите сервис:

```sh
sudo curl -L -o /opt/goip-bridge/goip-bridge.new https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
sudo chmod +x /opt/goip-bridge/goip-bridge.new
sudo chown goip-bridge:goip-bridge /opt/goip-bridge/goip-bridge.new
sudo mv /opt/goip-bridge/goip-bridge.new /opt/goip-bridge/goip-bridge
sudo systemctl restart goip-bridge
sudo journalctl -u goip-bridge -n 20 --no-pager
```

После перезапуска первой строкой лога будет баннер с новой версией. Проверить версию без логов:

```sh
/opt/goip-bridge/goip-bridge -version
```

Что важно при обновлении:

- `config.json` обратно совместим: новые версии не ломают старый конфиг, новые поля берут значения по умолчанию. Меняйте конфиг только чтобы включить новую возможность - смотрите release notes.
- Команды установки (`useradd`, `daemon-reload`, `enable`) повторять не нужно - только подмена бинарника и `restart`.
- Unit-файл обновляйте, только если в release notes указано, что он изменился.

### Создание и подключение MySQL

Без MySQL bridge работает через HTTP API и webhook. MySQL нужен, если приложение хочет очередь исходящих SMS в таблице и постоянное хранение входящих.

Готовый бинарник не содержит файла схемы - скачайте его из репозитория:

```sh
cd /opt/goip-bridge
sudo curl -L -o mysql.schema.sql https://raw.githubusercontent.com/e-u-shapovalov/goip-bridge/main/mysql.schema.sql
```

Создайте базу, пользователя и таблицы:

```sh
sudo mysql < mysql.schema.sql
```

Команда создаёт базу `goip_go`, пользователя `goip_bridge@127.0.0.1` и таблицы `goip_inbox` и `goip_outbox`.

Схема выдаёт пользователю временный пароль `CHANGE_ME_STRONG_DB_PASSWORD`. Замените его на свой:

```sql
ALTER USER 'goip_bridge'@'127.0.0.1' IDENTIFIED BY 'СИЛЬНЫЙ_ПАРОЛЬ';
FLUSH PRIVILEGES;
```

Добавьте блок `db` в `config.json` (если конфиг создан через `-init`, раскомментируйте готовый блок) и впишите тот же пароль:

```json
"db": {
  "host": "127.0.0.1",
  "port": 3306,
  "user": "goip_bridge",
  "password": "СИЛЬНЫЙ_ПАРОЛЬ",
  "name": "goip_go",
  "inbox_table": "goip_inbox",
  "outbox_table": "goip_outbox",
  "poll_sec": 3
}
```

Перезапустите сервис:

```sh
sudo systemctl restart goip-bridge
```

Проверьте, что база подключилась - в логе появится строка вида `db: connected to goip_bridge@127.0.0.1:3306/goip_go — inbox table ... + outbox queue ... active`:

```sh
sudo journalctl -u goip-bridge -n 20 --no-pager
```

Если вместо этого видите `db: configured but NOT connected ... retrying in background`, проверьте пароль, права пользователя и доступность MySQL. Пока база недоступна, `/sms` и `/ussd` в режиме очереди возвращают `503`. Подробнее: [MYSQL.md](MYSQL.md) и [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

Полная схема таблиц, права пользователя и примеры `INSERT`: [MYSQL.md](MYSQL.md).

**goip-bridge** - это легкий серверный шлюз для аппаратных GSM-шлюзов **GoIP DBL / Hybertone**. Он подключает GoIP к современному приложению: принимает SMS, отправляет SMS, выполняет USSD-запросы, отдает HTTP API, отправляет webhook и, при необходимости, работает с MySQL-очередью входящих и исходящих сообщений.

Если коротко: вы запускаете один бинарный файл на Linux-сервере, указываете этот сервер в настройках **SMS Server** на GoIP и получаете понятный **GoIP SMS API** для CRM, биллинга, Telegram-бота, мониторинга, helpdesk или своего backend-сервиса.

English version: [README.en.md](README.en.md)

## Скачать за 10 секунд

Обычному пользователю не нужен Git и не нужна сборка из исходников.

1. Откройте **GitHub Releases**: <https://github.com/e-u-shapovalov/goip-bridge/releases>
2. Откройте последний релиз, сейчас это **v0.3.2**.
3. В блоке **Assets** скачайте файл **`goip-bridge`**.
4. **Не скачивайте `Source code (zip)` и `Source code (tar.gz)`**, если хотите просто запустить программу.
5. **Не нажимайте `Code -> Download ZIP`** - это исходный код, а не готовая программа.

Прямая ссылка на готовый бинарник для Linux x86-64 / amd64 (всегда указывает на последний релиз):

<https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge>

Подробно для новичка: [DOWNLOAD.md](DOWNLOAD.md)

## Что решает проект

GoIP умеет работать как SMS-шлюз, но интеграция часто превращается в ручной веб-интерфейс, старые скрипты, `goipcron`, MySQL-таблицы непонятного формата и хрупкую обвязку. `goip-bridge` делает схему проще:

```text
GoIP DBL / Hybertone -> goip-bridge -> HTTP API / webhook / MySQL -> ваше приложение
```

Вместо ручной проверки SMS в панели GoIP вы получаете JSON-запросы, webhook-события и очередь сообщений, которую можно подключить к любой системе.

## Возможности

- Прием регистрации линий GoIP по UDP SMS Server protocol, по умолчанию порт `44444`.
- `GET /lines` - список линий, сигнал, IMEI/IMSI/ICCID, оператор, статус `alive`.
- Прием входящих SMS от GoIP.
- `/inbox` - последние 500 входящих SMS в памяти процесса.
- Webhook для входящих SMS и delivery reports, DLR.
- Надёжная доставка webhook: очередь в памяти, retry с exponential backoff, запись недоставленных событий в fallback-журнал.
- `POST /sms` - отправка SMS через HTTP API.
- `POST /ussd` - USSD-запросы, например проверка баланса.
- `GET /status/{id}` - статус асинхронного задания из MySQL-очереди.
- `DELETE /message/{id}` - отмена задания, пока оно ещё `queued`.
- `GET /health` - лёгкая проверка процесса, линий и MySQL без токена.
- Bearer-токен для защиты HTTP API.
- Опциональная MySQL-интеграция:
  - входящие SMS пишутся в `goip_inbox`;
  - исходящие SMS берутся из очереди `goip_outbox`;
  - SMS-статусы обновляются как `queued`, `sending`, `sent`, `delivered`, `failed`, `cancelled`;
  - USSD-задания получают `done` и ответ в поле `reply`.
- Ограничение источников UDP через `allow_src`.
- Темп отправки в MySQL-очереди через `send_pacing` и выбор линий через `default_lines`.
- Файловые логи, `debug`, `debug_line`, ротация по `log_max_mb`, fallback-журнал `goip-bridge.fallback.jsonl`.
- Генерация комментированного конфига: `-init ru` или `-init en`.
- Один исполняемый файл для Linux x86-64 / amd64.
- Подходит как замена старой связке `goipcron + Apache/PHP + MySQL`, если вам нужен более понятный сервис.

Используйте проект только для законных сценариев и с согласия получателей сообщений.

## Кому это нужно

- Владельцам GoIP DBL / Hybertone, которым нужен нормальный **GoIP SMS gateway**.
- Разработчикам CRM, ботов, личных кабинетов, мониторинга и внутренних панелей.
- Администраторам, которые хотят принимать SMS с SIM-карт в свой backend.
- Командам, которым нужна отправка сервисных SMS через собственный GoIP.
- Тем, кто хочет заменить ручной веб-интерфейс GoIP на HTTP API и webhook.
- Тем, кому нужна MySQL-очередь SMS, но без старого `goipcron`.

## Схема работы

```mermaid
flowchart LR
    A[GoIP DBL / Hybertone] -->|UDP SMS Server :44444| B[goip-bridge]
    B -->|HTTP API: /sms /ussd /lines /inbox| C[CRM / bot / backend]
    B -->|webhook: SMS + DLR| D[Ваш webhook]
    B <-->|optional MySQL inbox/outbox| E[(MySQL / MariaDB)]
```

Для первого запуска полезнее смотреть схему как чеклист:

```mermaid
flowchart TD
    A[Скачать goip-bridge из Releases -> Assets] --> B[chmod +x goip-bridge]
    B --> C[Создать config.json]
    C --> D[Запустить ./goip-bridge -config config.json]
    D --> E[Открыть UDP 44444 в firewall]
    E --> F[В GoIP включить SMS Server]
    F --> G[curl GET /lines]
    G --> H{alive=true?}
    H -- да --> I[POST /sms или MySQL queue]
    H -- нет --> J[Проверить IP, порт, firewall, route, GSM LOGIN]
```

Больше схем для студентов и администраторов: [SCHEMES.md](SCHEMES.md)

Пример реальной страницы GoIP SMS:

![GoIP SMS Server settings](docs/screenshots/goip-sms-server-settings.png)

Рабочие поля на странице: `SMS Server`, `SMS Server IP`, `SMS Server Port`, `SMS Client ID`, `Password`, `Save Changes`. Укажите IP своего Linux-сервера с goip-bridge и порт `44444`.

Скриншоты, которые полезно добавить в будущем:

- `docs/screenshots/lines-api-response.png` - пример ответа `GET /lines`.
- `docs/screenshots/mysql-outbox.png` - очередь исходящих SMS в MySQL.

## Быстрый запуск без MySQL

Это самый простой путь для первого запуска: HTTP API + webhook, без базы данных.

```sh
mkdir -p /opt/goip-bridge
cd /opt/goip-bridge
curl -L -o goip-bridge https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
chmod +x goip-bridge
```

Создайте файл `config.json` рядом с бинарником:

Самый простой способ:

```sh
./goip-bridge -config config.json -init ru
```

Команда создаст комментированный JSONC-конфиг и не перезапишет существующий файл. Если нужен английский вариант комментариев, используйте `-init en`.

Минимальный вариант без MySQL:

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
  "debug": false,
  "log_max_mb": 10,
  "line_dead_after_sec": 120,
  "allow_src": [],
  "line_passwords": {}
}
```

Что важно в конфиге:

- `http_token` - bearer-токен для HTTP API. Пустая строка = API без авторизации; если при этом `listen_http` не loopback, bridge выведет предупреждение в лог при старте.
- `line_passwords` - ручное закрепление пароля линии. Пароль используется при отправке SMS/USSD, а для перечисленных здесь линий ещё и проверяется на входящих keepalive/SMS/DLR. Если линия не перечислена, bridge принимает пароль из keepalive GoIP.
- `webhook_retry` - сколько и как долго повторять webhook, если ваш сервер временно недоступен.
- `ussd_retransmit_sec` не стоит делать слишком маленьким: bridge повторит USSD-команду, если оператор не ответил за это время, а слишком частые повторы создают новую USSD-сессию поверх ещё открытой и ломают ответ.
- `send_pacing` - пауза между заданиями на одной линии в MySQL-очереди; помогает не забивать SIM подряд и держит одну отправку на линию.
- `default_lines` - какие линии использовать для MySQL-строк без `line`; пустой список = все живые линии round-robin.
- `debug` - подробный лог приёма и отправки SMS и USSD (keepalive не логируется). Пишется в файлы рядом с `config.json`, см. `log_max_mb`.
- `debug_line` - отдельный сырой keepalive-лог по каждой линии, включая пароль и SIM-идентификаторы; включайте только для диагностики.
- `log_max_mb` - размер каждого лог-файла до ротации, МБ (по умолчанию 10). Bridge пишет `goip-bridge.log` и `goip-bridge.err.log` рядом с конфигом; при превышении старый файл переименовывается в `.log.1` (хранится одна предыдущая копия). Файлы создаются с правами `0600`, так как содержат номера и тексты SMS.
- `line_dead_after_sec` - через сколько секунд без keepalive линия считается «не живой» (по умолчанию 120). Влияет на поле `alive` в `/lines`, на `/health` и на авто-выбор линии при пустом `line`.
- `allow_src` - список IP или CIDR, с которых принимаются UDP-пакеты от GoIP, например `["10.0.0.200", "192.168.1.0/24"]`. Пустой список = принимать с любого адреса (тогда единственная защита - firewall). Пакеты с других адресов молча отбрасываются.

Полный справочник по каждой настройке: [CONFIG.md](CONFIG.md)

Запустите:

```sh
./goip-bridge -config config.json
```

В логе должны появиться строки примерно такого вида. Первая строка - это «первое эхо»: она показывает название, версию и копирайт того бинарника, который реально запустился.

```text
https://github.com/e-u-shapovalov/goip-bridge
(c) Evgenii Shapovalov 2026 v0.3.2
2026/06/11 10:00:00 logging to /opt/goip-bridge (goip-bridge.log + .err.log, cap 10 MB, debug=false debug_line=false)
2026/06/11 10:00:00 goip-bridge listening on UDP :44444 (GoIP lines register here)
2026/06/11 10:00:00 HTTP API on 127.0.0.1:8080
```

Версию также можно узнать без запуска сервиса:

```sh
./goip-bridge -version
```

## Что настроить в GoIP

В веб-интерфейсе GoIP откройте настройки SMS нужной линии или канала и укажите:

```text
SMS Server: Enable
SMS Server IP: IP-адрес Linux-сервера, где запущен goip-bridge
SMS Server Port: 44444
SMS Client ID: идентификатор линии, например Go1
Password: пароль линии
```

После изменения нажмите **Save Changes**. Если есть кнопка `*Auto Config Other lines`, нажимайте ее только когда хотите применить похожие настройки к другим каналам GoIP.

После этого GoIP должен отправить keepalive на `goip-bridge`. Проверка:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Если все хорошо, вы увидите JSON со строкой линии и `"alive": true`.

## HTTP API

Если в `config.json` задан `http_token`, добавляйте заголовок:

```text
Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN
```

Список линий:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Отправить SMS:

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"996700000001","text":"Test message"}'
```

Если `line` оставить пустой без MySQL, bridge выберет живую линию с наименьшим `id`. Для production лучше указывать конкретную линию, например `Go1`.

Важно: если GoIP не подтвердил отправку, `/sms` все равно может вернуть HTTP `200`, но с JSON `{"status":"failed","error":"..."}`. В интеграции проверяйте поле `status`, а не только HTTP-код.

Если в конфиге включён MySQL, `/sms` и `/ussd` работают асинхронно: API возвращает HTTP `202` и `id`, а результат смотрится через `GET /status/{id}`, webhook или SQL-строку. Пока MySQL настроен, но временно недоступен, `/sms` и `/ussd` возвращают `503`, чтобы не отправить сообщение в обход очереди.

USSD:

```sh
curl -X POST http://127.0.0.1:8080/ussd \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","code":"*100#"}'
```

Последние входящие SMS:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/inbox
```

Полное описание API: [API.md](API.md)

## MySQL-режим

MySQL не обязателен. Если блок `db` отсутствует в `config.json`, `goip-bridge` работает только через HTTP API, webhook и память.

Если нужен привычный режим очереди:

- входящие SMS записываются в таблицу `goip_inbox`;
- исходящие SMS кладутся в `goip_outbox` со статусом `queued`;
- bridge сам забирает очередь, отправляет SMS и обновляет статус.

Быстро создать базу, пользователя и таблицы:

```sh
sudo mysql < mysql.schema.sql
```

Имена по умолчанию:

```text
database:      goip_go
db user:       goip_bridge
inbox table:   goip_inbox
outbox table:  goip_outbox
```

Подробная схема таблиц, SQL-команды и примеры `INSERT`: [MYSQL.md](MYSQL.md)

Схема MySQL-очереди: [SCHEMES.md#6-отправка-sms-через-mysql-очередь](SCHEMES.md#6-отправка-sms-через-mysql-очередь)

Runtime-детали MySQL-режима: максимум 8 открытых DB-соединений, одно активное SMS/USSD-задание на линию, задержка между заданиями через `send_pacing`, страницы очереди по 100 строк, DLR retry до 6 попыток с паузой 1.5 секунды.

Если MySQL временно недоступен, bridge не сдаётся: подключение к базе он повторяет в фоне каждые 15 секунд. А данные, которые не удалось записать за несколько попыток (входящая SMS, статус отправки, delivery report), дописываются в файл `goip-bridge.fallback.jsonl` рядом с конфигом - чтобы ничего не потерялось молча. Этот файл только для ручного разбора и не применяется к базе автоматически. Подробнее: [MYSQL.md](MYSQL.md).

## Установка как systemd-сервис

Для постоянной работы на сервере используйте systemd:

```sh
sudo useradd --system --home /opt/goip-bridge --shell /usr/sbin/nologin goip-bridge
sudo mkdir -p /opt/goip-bridge
sudo cp goip-bridge config.json /opt/goip-bridge/
sudo chown -R goip-bridge:goip-bridge /opt/goip-bridge
sudo cp goip-bridge.service /etc/systemd/system/goip-bridge.service
sudo systemctl daemon-reload
sudo systemctl enable --now goip-bridge
```

Логи:

```sh
sudo journalctl -u goip-bridge -f
```

Подробная установка: [INSTALL.md](INSTALL.md)

## Firewall и сеть

GoIP должен достучаться до сервера по **UDP `44444`**. Если firewall режет этот порт, `/lines` будет пустым.

Минимально для `ufw`:

```sh
sudo ufw allow 44444/udp
```

Для `nftables` правило нужно сохранить в `/etc/nftables.conf` и включить автозагрузку:

```sh
sudo nft -f /etc/nftables.conf
sudo systemctl enable --now nftables
sudo nft list ruleset | grep 44444
```

HTTP API `8080` открывайте только если API нужен с другой машины. MySQL/MariaDB `3306` обычно не открывают наружу, если база стоит на том же сервере.

Подробно: [FIREWALL.md](FIREWALL.md)

## Для разработчиков

Нужен Go **1.24** или новее, потому что `go.mod` проекта указывает `go 1.24.0`.

```sh
git clone https://github.com/e-u-shapovalov/goip-bridge.git
cd goip-bridge
go run . -config config.json -init ru   # создаст config.json с комментариями (или -init en)
# отредактируйте config.json, затем запустите:
go run . -config config.json
```

Сборка Linux x86-64 / amd64:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o goip-bridge .
```

Проверка зависимостей:

```sh
go mod tidy
go test ./...
```

## Ограничения

- Нужен аппаратный GoIP / DBL / Hybertone с поддержкой режима **SMS Server**.
- Это не SMPP-сервер. Проект дает HTTP API, webhook и MySQL-очередь поверх GoIP.
- `/inbox` хранит только последние 500 SMS в памяти. После перезапуска память очищается.
- Постоянное хранение входящих SMS включайте через MySQL.
- Длинные SMS обычно собирает или режет само устройство GoIP. Bridge работает с тем текстом, который получил от устройства.
- Проверяйте свою модель GoIP, прошивку, SIM-карты и оператора перед production-запуском.

## FAQ

### Мне нужен Git?

Нет. Git нужен только разработчику. Обычный пользователь скачивает файл `goip-bridge` из **Releases -> Assets**.

### Что именно скачивать на GitHub?

Скачивайте asset `goip-bridge` из последнего релиза (сейчас `v0.3.2`). Не скачивайте `Source code` и не используйте `Code -> Download ZIP`.

### Можно ли запустить на Windows?

Текущий готовый релиз опубликован как Linux x86-64 / amd64 бинарник. Запускайте его на Linux-сервере, VPS, мини-ПК или виртуальной машине в сети с GoIP.

### Нужен ли MySQL?

Нет, если вам хватает HTTP API, webhook и `/inbox` в памяти. MySQL нужен, если вы хотите очередь исходящих SMS в таблице и постоянное хранение входящих SMS.

### Как узнать имя линии?

Запустите bridge, настройте GoIP на `SMS Server IP` и `SMS Server Port`, затем выполните:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Поле `id` - это имя линии, например `Go1`.

### Где смотреть логи?

Если запустили вручную, логи идут прямо в терминал. Если запустили через systemd:

```sh
sudo journalctl -u goip-bridge -f
```

## Диагностика

Коротко:

- `unauthorized` - неправильный `Authorization: Bearer ...`.
- `/lines` пустой - GoIP не дошел до UDP-порта `44444`.
- `no alive line` - нет зарегистрированной линии со статусом `LOGIN`.
- `ussd timeout` - устройство или оператор не ответили за `ussd_timeout_sec`.
- `queue temporarily unavailable (db reconnecting)` - блок `db` есть, но MySQL сейчас недоступна; `/sms` и `/ussd` возвращают `503`, а bridge повторяет подключение каждые 15 секунд.

Полный разбор проблем: [TROUBLESHOOTING.md](TROUBLESHOOTING.md)

Firewall, `nftables`, `ufw`, маршруты и проверка после ребута: [FIREWALL.md](FIREWALL.md)

Визуальные схемы запуска, портов, MySQL и systemd: [SCHEMES.md](SCHEMES.md)

Релизы и публикация assets: [RELEASES.md](RELEASES.md)

## SEO: как это ищут

Проект закрывает задачи, которые обычно ищут как **GoIP SMS API**, **GoIP SMS gateway**, **GoIP HTTP API**, **GoIP webhook incoming SMS**, **GoIP USSD API**, **отправка SMS через GoIP**, **прием SMS с GoIP**, **HTTP API для GSM шлюза**, **MySQL очередь SMS**, **замена goipcron**, **GoIP без Apache/PHP**, **GSM modem SMS gateway**.

Текст использует реальные термины проекта без спама: GoIP, DBL, Hybertone, SMS Server, UDP keepalive, HTTP API, webhook, DLR, USSD, MySQL inbox/outbox.

## Релизы

Опубликованы релизы `v0.1.0`, `v0.2.0`, `v0.3.0` и `v0.3.1`. Последний релиз `v0.3.2` содержит готовый asset `goip-bridge` размером около 10 MB для Linux x86-64 / amd64.

Страница релизов: <https://github.com/e-u-shapovalov/goip-bridge/releases>

## Лицензия и связь

Проект распространяется под MIT License: [LICENSE](LICENSE).

Автор и репозиторий: <https://github.com/e-u-shapovalov/goip-bridge>

Для багов, вопросов и предложений используйте GitHub Issues.

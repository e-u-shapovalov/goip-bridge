# Диагностика goip-bridge

Этот файл помогает понять, где сломалось: скачивание, запуск, сеть, GoIP, HTTP API, USSD, webhook или MySQL.

## Где смотреть логи

Если запустили вручную:

```sh
./goip-bridge -config config.json
```

Логи идут прямо в этот терминал.

Если запустили через systemd:

```sh
sudo journalctl -u goip-bridge -f
```

Последние 100 строк:

```sh
sudo journalctl -u goip-bridge -n 100 --no-pager
```

Статус сервиса:

```sh
sudo systemctl status goip-bridge
```

## Быстрый чек сервера

Проверить, что процесс слушает UDP и HTTP:

```sh
ss -lunpt | grep -E '44444|8080'
```

Проверить firewall:

```sh
sudo nft list ruleset | grep 44444
sudo systemctl is-enabled nftables
sudo systemctl status nftables
```

Проверить HTTP API:

```sh
curl -i -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Проверить, что файл запускаемый:

```sh
ls -lh goip-bridge
```

Если нет `x` в правах, выполните:

```sh
chmod +x goip-bridge
```

## Проблема: `permission denied`

Причина: Linux не разрешает запуск файла.

Решение:

```sh
chmod +x goip-bridge
./goip-bridge -config config.json
```

## Проблема: `config: open config.json: no such file or directory`

Причина: рядом нет конфига или вы запускаете из другой папки.

Решение:

```sh
pwd
ls
./goip-bridge -config config.json
```

Если конфиг лежит в другом месте:

```sh
./goip-bridge -config /opt/goip-bridge/config.json
```

## Проблема: `listen udp :44444: bind: address already in use`

Причина: порт `44444/udp` уже занят другим процессом.

Проверка:

```sh
sudo ss -lunp | grep 44444
```

Решение: остановите второй процесс или поменяйте `listen_udp` в `config.json` и настройках GoIP.

## Проблема: `unauthorized`

Причина: токен в запросе не совпадает с `http_token`.

Правильный формат:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Проверьте:

- в `config.json` нет лишних пробелов в токене;
- после изменения конфига сервис перезапущен;
- вы используете тот же токен в curl.

Перезапуск systemd:

```sh
sudo systemctl restart goip-bridge
```

## Проблема: `/lines` возвращает `[]`

Bridge работает, но GoIP еще не зарегистрировал ни одну линию.

Проверьте на GoIP:

```text
SMS Server IP: IP сервера с goip-bridge
SMS Server Port: 44444
SMS Client ID / Password: заполнены для линии
```

Проверьте сеть:

- GoIP и сервер видят друг друга;
- UDP-порт `44444` открыт;
- firewall не режет UDP;
- если сервер за NAT, проброшен UDP-порт;
- в `listen_udp` указан правильный адрес.

Команда для UFW:

```sh
sudo ufw allow 44444/udp
```

Команды для `nftables`:

```sh
sudo nft list ruleset | grep 44444
sudo systemctl enable --now nftables
```

Подробно по firewall, серым IP, маршрутам и проверке после ребута: [FIREWALL.md](FIREWALL.md)

## Проблема: линия есть, но `"alive": false`

GoIP прислал keepalive, но статус GSM не `LOGIN`.

Проверьте:

- SIM-карта вставлена;
- SIM не требует PIN;
- есть GSM-сигнал;
- устройство зарегистрировано в сети оператора;
- на GoIP нет ошибки по каналу.

## Проблема: `no alive line`

HTTP API не нашел живую линию для отправки.

Решение:

1. Проверьте `/lines`.
2. Убедитесь, что нужная линия имеет `"alive": true`.
3. В `/sms` укажите правильный `line`, например `Go1`.
4. Если линию указывать не хотите, отправьте `"line": ""`, тогда будет выбрана первая живая.

## Проблема: SMS возвращает `timeout`

Bridge начал отправку, но GoIP не завершил протокол за `send_timeout_sec`.

Проверьте:

- пароль линии в GoIP;
- SIM зарегистрирована в сети;
- хватает баланса;
- номер получателя в правильном формате;
- GoIP не занят другим действием;
- UDP-пакеты между GoIP и сервером не теряются.

Можно временно увеличить:

```json
"send_timeout_sec": 90
```

Важно: `/sms` может вернуть HTTP `200` и при ошибке отправки. В этом случае в JSON будет `status: "failed"` и поле `error`. Клиент должен проверять `status`.

## Проблема: USSD возвращает `ussd timeout`

Проверьте:

- USSD-код существует у оператора;
- SIM видит сеть;
- баланс не заблокирован;
- GoIP не занят отправкой SMS;
- `ussd_timeout_sec` достаточно большой.

Рекомендуемые значения:

```json
"ussd_timeout_sec": 120,
"ussd_retransmit_sec": 60
```

Не ставьте слишком маленький `ussd_retransmit_sec`: частые повторные USSD-команды могут ломать сессию.

## Проблема: webhook не приходит

Проверьте конфиг:

```json
{
  "webhook_url": "https://example.com/goip-webhook",
  "webhook_token": "WEBHOOK_SECRET"
}
```

Проверьте с сервера:

```sh
curl -i https://example.com/goip-webhook
```

Частые причины:

- URL недоступен с сервера;
- ошибка TLS/HTTPS;
- firewall блокирует исходящий запрос;
- принимающий сервис долго отвечает;
- принимающий сервис требует другой токен;
- webhook принимает только определенный HTTP-метод или путь.

Bridge отправляет `POST` с `Content-Type: application/json`.

Webhook timeout в текущей версии - 15 секунд. Если принимающая сторона не ответила за это время, bridge пишет `webhook error` в лог и не повторяет отправку события.

## Проблема: `WARNING: MySQL connect failed, retrying in background`

В `config.json` есть блок `db`, но подключение не удалось.

Bridge продолжит работать без MySQL (HTTP API и webhook останутся доступны) и будет повторять подключение к базе каждые 15 секунд в фоне. Как только доступ восстановится, в логе появится `MySQL connected (after retry)` - перезапуск не нужен.

Пока база недоступна, входящие SMS, статусы отправки и delivery report, которые не удалось записать, дописываются в `goip-bridge.fallback.jsonl` рядом с конфигом (см. [MYSQL.md](MYSQL.md)) - чтобы данные не потерялись молча.

Проверьте:

- MySQL/MariaDB запущен;
- `host`, `port`, `user`, `password`, `name` верные;
- база существует;
- пользователь имеет права `SELECT`, `INSERT`, `UPDATE`;
- таблицы созданы.

Команды:

```sh
sudo systemctl status mysql
sudo systemctl status mariadb
```

Подключение вручную:

```sh
mysql -h 127.0.0.1 -P 3306 -u goip_bridge -p goip_go
```

Схемы таблиц: [MYSQL.md](MYSQL.md)

## Проблема: сообщения в MySQL не отправляются

Проверьте очередь:

```sql
SELECT id, line, to_number, status, error_code, created_at, sent_at, delivered_at
FROM goip_outbox
ORDER BY id DESC
LIMIT 20;
```

Если статус `queued`:

- bridge не подключился к MySQL;
- `poll_sec` еще не прошел;
- нет живой линии;
- строка не попала в правильную таблицу.

Если статус `failed`, смотрите `error_code`.

## Проблема: HTTP API доступен только с самого сервера

По умолчанию:

```json
"listen_http": "127.0.0.1:8080"
```

Это значит, что API слушает только localhost.

Если API нужен с другой машины:

```json
"listen_http": "0.0.0.0:8080"
```

После изменения:

```sh
sudo systemctl restart goip-bridge
```

Не открывайте API в интернет без токена, firewall или VPN.

## Проблема: скачал `Source code`, а не программу

Скачайте готовый бинарник из релиза:

<https://github.com/e-u-shapovalov/goip-bridge/releases/download/v0.2.0/goip-bridge>

Потом:

```sh
chmod +x goip-bridge
./goip-bridge -config config.json
```

## Что отправить автору при баге

Не отправляйте пароли и токены. Полезная информация:

- версия релиза, например `v0.2.0`;
- модель GoIP и прошивка;
- фрагмент `config.json` без секретов;
- вывод `GET /lines`;
- последние 50-100 строк лога;
- какая команда curl выполнялась;
- что ожидали и что получили.

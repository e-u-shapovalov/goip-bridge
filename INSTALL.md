# Установка, настройка и запуск goip-bridge

Это практическая инструкция для администратора или разработчика, который подключает GoIP к HTTP API через `goip-bridge`.

## Требования

- Аппаратный GSM-шлюз GoIP / DBL / Hybertone с поддержкой режима **SMS Server**.
- Linux x86-64 / amd64 для готового релиза.
- Доступ по сети от GoIP до сервера с `goip-bridge` по UDP-порту `44444`.
- Доступ к HTTP API bridge с той машины, где будет работать ваша CRM, бот или backend.
- Для сборки из исходников: Go 1.21 или новее.

## Установка готового релиза

1. Скачайте готовый файл из **GitHub Releases -> Assets**. Не скачивайте `Source code`.
2. Положите файл в рабочую папку, например:

```sh
mkdir -p /opt/goip-bridge
cd /opt/goip-bridge
```

3. Если скачан архив:

```sh
tar -xzf goip-bridge-linux-amd64.tar.gz
```

4. Сделайте файл исполняемым:

```sh
chmod +x goip-bridge
```

5. Создайте `config.json` рядом с бинарником.

Минимальный конфиг:

```json
{
  "listen_udp": ":44444",
  "listen_http": "127.0.0.1:8080",
  "http_token": "CHANGE_ME",
  "webhook_url": "",
  "webhook_token": "",
  "send_timeout_sec": 45,
  "ussd_timeout_sec": 60,
  "retransmit_sec": 5,
  "line_passwords": {}
}
```

6. Запустите:

```sh
./goip-bridge -config config.json
```

В логе должно появиться, что bridge слушает UDP и HTTP API.

## Настройка GoIP

В веб-интерфейсе GoIP откройте настройки SMS для нужной линии или канала.

Укажите:

```text
SMS Server IP: IP-адрес сервера с goip-bridge
SMS Server Port: 44444
Client ID: идентификатор линии, например Go1
Password: пароль линии
```

`goip-bridge` умеет подхватывать пароль из keepalive. Если нужно принудительно задать пароль для линии, используйте `line_passwords`:

```json
{
  "line_passwords": {
    "Go1": "secret-password"
  }
}
```

## Настройка HTTP API

По умолчанию API слушает только локальный адрес:

```json
"listen_http": "127.0.0.1:8080"
```

Это безопаснее, если API вызывает приложение на том же сервере.

Если API должен быть доступен с другой машины, можно слушать все интерфейсы:

```json
"listen_http": "0.0.0.0:8080"
```

В этом случае обязательно задайте сильный `http_token` и ограничьте доступ firewall или reverse proxy.

## Проверка работы

Список линий:

```sh
curl -H "Authorization: Bearer CHANGE_ME" http://127.0.0.1:8080/lines
```

Отправка SMS:

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"996700000001","text":"Test message"}'
```

USSD:

```sh
curl -X POST http://127.0.0.1:8080/ussd \
  -H "Authorization: Bearer CHANGE_ME" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","code":"*100#"}'
```

Входящие SMS:

```sh
curl -H "Authorization: Bearer CHANGE_ME" http://127.0.0.1:8080/inbox
```

## Webhook

Чтобы получать входящие SMS и DLR во внешнем приложении, укажите URL:

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
  "state": "DELIVRD",
  "time": "2026-06-09T18:00:00Z"
}
```

## Сборка из исходников

```sh
git clone https://github.com/e-u-shapovalov/goip-bridge.git
cd goip-bridge
cp config.example.json config.json
go run . -config config.json
```

Статический бинарник Linux amd64:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o goip-bridge .
```

## Диагностика

### Не появляются линии в `/lines`

Проверьте:

- GoIP отправляет SMS Server keepalive на правильный IP сервера.
- UDP-порт `44444` открыт на сервере.
- GoIP и сервер находятся в одной доступной сети или правильно настроен маршрутизатор.
- В настройках GoIP выбран нужный SMS Server IP/Port.

### SMS не отправляется, ответ `no alive line`

Bridge не видит активную линию. Сначала добейтесь, чтобы `/lines` показывал `alive: true`.

### SMS возвращает `timeout`

UDP-пакеты могут теряться или блокироваться. Проверьте сеть, firewall, правильность пароля линии и статус SIM-карты.

### USSD возвращает ошибку или timeout

Проверьте, что USSD-код поддерживается оператором, SIM зарегистрирована в сети, баланс достаточный, а `ussd_timeout_sec` не слишком маленький.

### Webhook не вызывается

Проверьте доступность `webhook_url` с сервера, корректность TLS/HTTPS, firewall и то, что принимающая сторона возвращает HTTP-ответ без долгого ожидания.

## Production notes

- Не публикуйте HTTP API в интернет без токена и сетевых ограничений.
- Храните `config.json` с токенами вне публичного доступа.
- Настройте supervisor, systemd или другой менеджер процессов, чтобы bridge автоматически перезапускался.
- Логи процесса важны для диагностики UDP-сессий и webhook-ошибок.
- Перед рабочей эксплуатацией протестируйте свою модель GoIP, свою прошивку и своего оператора.

## English summary

Install the ready Linux amd64 binary from **GitHub Releases -> Assets**, create `config.json`, run `./goip-bridge -config config.json`, then configure the GoIP channel's **SMS Server IP/Port** to point to this service. Do not download `Source code` unless you plan to build from source.

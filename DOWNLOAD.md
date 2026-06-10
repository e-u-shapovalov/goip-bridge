# Как скачать goip-bridge с GitHub и не перепутать с исходным кодом

Эта инструкция для человека, который не знает Git, Go и GitHub, но хочет скачать готовую программу и запустить ее на Linux-сервере.

Главное правило: **обычному пользователю нужен файл из `Releases -> Assets`, а не `Code -> Download ZIP`**.

## Самая короткая инструкция

1. Откройте страницу релизов: <https://github.com/e-u-shapovalov/goip-bridge/releases>
2. Нажмите последний релиз, сейчас это **v0.3.0**.
3. Найдите блок **Assets**.
4. Скачайте файл **`goip-bridge`**.
5. Не скачивайте `Source code (zip)` и `Source code (tar.gz)`.

Прямая ссылка на текущий готовый бинарник:

<https://github.com/e-u-shapovalov/goip-bridge/releases/download/v0.3.0/goip-bridge>

## Что скачивать

Правильный файл для Linux x86-64 / amd64:

```text
goip-bridge
```

В будущих релизах имя может быть более подробным:

```text
goip-bridge-linux-amd64
goip-bridge-linux-amd64.tar.gz
```

Если видите несколько файлов, выбирайте тот, где есть `goip-bridge` и `linux-amd64`.

## Что не скачивать

Не скачивайте эти файлы, если хотите просто запустить программу:

```text
Source code (zip)
Source code (tar.gz)
Code -> Download ZIP
```

Это исходный код. Внутри может не быть готового исполняемого файла для вашего сервера. Такой вариант нужен разработчикам, которые умеют собирать Go-проект.

## Где на GitHub находится Releases

На странице репозитория GitHub обычно показывает блок **Releases** справа от списка файлов. На телефоне или узком экране этот блок может быть ниже.

Путь словами:

```text
страница проекта -> правая колонка -> Releases -> Latest -> Assets -> goip-bridge
```

Если вы открыли релиз и не видите **Assets**, прокрутите страницу ниже текста релиза.

## Как скачать прямо с Linux-сервера

Если вы уже подключились к серверу по SSH, можно скачать без браузера:

```sh
mkdir -p /opt/goip-bridge
cd /opt/goip-bridge
curl -L -o goip-bridge https://github.com/e-u-shapovalov/goip-bridge/releases/download/v0.3.0/goip-bridge
chmod +x goip-bridge
```

Проверка, что файл на месте:

```sh
ls -lh goip-bridge
```

Должен быть файл размером около 10 MB.

## Что делать после скачивания

Создайте рядом файл `config.json`:

```sh
nano config.json
```

Вставьте минимальный конфиг:

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

Поменяйте `CHANGE_ME_TO_LONG_RANDOM_TOKEN` на свой длинный секретный токен.

Запуск:

```sh
./goip-bridge -config config.json
```

Если видите такие строки, программа стартовала. Первая строка показывает версию, которая запустилась:

```text
goip-bridge v0.3.0 — GoIP SMS/USSD gateway. Copyright (c) 2026 Evgenii Shapovalov
logging to /opt/goip-bridge (goip-bridge.log + .err.log, cap 10 MB, debug=false)
goip-bridge listening on UDP :44444 (GoIP lines register here)
HTTP API on 127.0.0.1:8080
```

## Что настроить в GoIP

В веб-интерфейсе GoIP найдите настройки SMS нужного канала и укажите:

```text
SMS Server IP: IP-адрес сервера с goip-bridge
SMS Server Port: 44444
SMS Client ID: например Go1
Password: пароль этой линии
```

После этого проверьте линии:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Если вы запускали API не на том же сервере или поменяли `listen_http`, замените адрес `127.0.0.1:8080` на свой.

## Частые ошибки при скачивании

### Я скачал ZIP, но не понимаю, что запускать

Вы скачали исходный код. Вернитесь в **Releases -> Assets** и скачайте файл `goip-bridge`.

### В Assets есть только Source code

Значит к этому релизу не прикрепили готовый бинарник. Для `v0.3.0` готовый asset `goip-bridge` опубликован. Для будущих релизов проверяйте блок **Assets**.

### Windows не запускает файл

Текущий релиз - для Linux x86-64 / amd64. Его нужно запускать на Linux-сервере, VPS, мини-ПК или виртуальной машине, которая находится в сети с GoIP.

### Браузер предупреждает о неизвестном файле

Для нового open-source бинарника это может быть нормально. Скачивайте только с официальной страницы релиза проекта:

<https://github.com/e-u-shapovalov/goip-bridge/releases>

### Permission denied

Вы забыли дать право на запуск:

```sh
chmod +x goip-bridge
```

### No such file or directory

Вы запускаете команду не из той папки. Перейдите туда, где лежит файл:

```sh
cd /opt/goip-bridge
ls
./goip-bridge -config config.json
```

## Следующий шаг

После скачивания переходите к полной установке: [INSTALL.md](INSTALL.md)

English note: download the ready binary from **GitHub Releases -> Assets**. Do not use `Code -> Download ZIP` unless you want the source code.

# Релизы goip-bridge

Этот файл для автора проекта и для пользователя, который пытается понять, что именно скачивать на GitHub.

## Что уже опубликовано

Проверено по GitHub API:

- `v0.2.0` - актуальный релиз с MySQL-интеграцией.
- `v0.1.0` - первый pre-release.

В `v0.2.0` в **Assets** прикреплен готовый файл:

```text
goip-bridge
```

Это Linux x86-64 / amd64 бинарник размером около 10 MB.

Страница релизов:

<https://github.com/e-u-shapovalov/goip-bridge/releases>

## Что скачивать обычному пользователю

Обычный пользователь должен открыть:

```text
Releases -> Latest -> Assets -> goip-bridge
```

И не должен скачивать:

```text
Source code (zip)
Source code (tar.gz)
Code -> Download ZIP
```

`Source code` нужен разработчику, а не человеку, который хочет запустить готовую программу.

## Рекомендуемые имена assets

Сейчас файл называется просто:

```text
goip-bridge
```

Для следующих релизов понятнее использовать имя с платформой:

```text
goip-bridge-linux-amd64
goip-bridge-linux-amd64.tar.gz
checksums.txt
```

Если появятся сборки под другие платформы:

```text
goip-bridge-linux-arm64
goip-bridge-windows-amd64.exe
```

Не публикуйте непонятные имена вроде `main`, `app`, `build` или `release`.

## Чеклист перед публикацией релиза

1. Обновить `CHANGELOG.md`.
2. Проверить `README.md`, `README.en.md`, `INSTALL.md`, `DOWNLOAD.md`, `SCHEMES.md`, `MYSQL.md`, `FIREWALL.md`, `TROUBLESHOOTING.md`.
3. Собрать бинарник:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o goip-bridge .
```

4. Проверить запуск:

```sh
./goip-bridge -config config.no-mysql.example.json
```

5. Проверить, что в release assets есть готовый бинарник, а не только `Source code`.
6. В тексте релиза явно написать: "Если вы обычный пользователь, скачайте `goip-bridge` из Assets".
7. Проверить, что `mysql.schema.sql` соответствует полям, которые реально использует код.
8. Проверить, что скриншоты не содержат серийники, IMEI, публичные IP, токены или другие секреты.

## Шаблон текста релиза

```md
## goip-bridge vX.Y.Z

GoIP SMS/USSD gateway for GoIP DBL / Hybertone: HTTP API, incoming SMS webhook and optional MySQL inbox/outbox queue.

### Что скачать

Если вы обычный пользователь, скачайте готовый файл из **Assets**:

- `goip-bridge-linux-amd64`

Не скачивайте `Source code (zip)` и `Source code (tar.gz)`, если хотите просто запустить программу.

### Быстрый запуск

chmod +x goip-bridge-linux-amd64
mv goip-bridge-linux-amd64 goip-bridge
./goip-bridge -config config.json

### Что изменилось

- ...

### Документация

- README: https://github.com/e-u-shapovalov/goip-bridge
- Install: INSTALL.md
- Schemes: SCHEMES.md
- API: API.md
- MySQL: MYSQL.md
- Firewall: FIREWALL.md
- Troubleshooting: TROUBLESHOOTING.md
```

## Что желательно добавить

- `LICENSE`.
- `checksums.txt` с SHA256.
- Архив `tar.gz`, где лежат бинарник, `config.no-mysql.example.json`, `config.example.json`, `README.md`.
- Скриншот настройки SMS Server в GoIP.
- Краткое видео или GIF первого запуска.

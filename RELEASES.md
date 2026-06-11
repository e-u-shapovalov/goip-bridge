# Релизы goip-bridge

Этот файл для автора проекта и для пользователя, который пытается понять, что именно скачивать на GitHub.

## Что уже опубликовано

Проверено по GitHub API:

- `v0.4.0` - актуальный релиз: webhook-мониторинг линий, события отправки в любом режиме, запрет редиректов webhook, `-update`, `check_updates`, `clear_logs_on_start`, баннер-рамка.
- `v0.3.2` - исправления по итогам ревью и первые модульные тесты.
- `v0.3.1` - download-ссылки переведены на постоянный пермалинк `latest`.
- `v0.3.0` - надёжность, безопасность, наблюдаемость, документация.
- `v0.2.0` - MySQL-интеграция.
- `v0.1.0` - первый pre-release.

В последних релизах в **Assets** прикреплен готовый файл:

```text
goip-bridge
```

Это Linux x86-64 / amd64 бинарник размером около 11 MB.

Локально для следующей публикации также подготовлены:

```text
goip-bridge-linux-amd64.tar.gz
checksums.txt
```

Архив содержит бинарник, `config.example.jsonc`, `config.no-mysql.example.json`,
основные Markdown-инструкции, `CHANGELOG.md`, `mysql.schema.sql`,
`goip-bridge.service` и `LICENSE`.

Постоянная ссылка на актуальный бинарник:

```text
https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
```

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

### Боевой режим — релиз одним скриптом

Скрипт `prepare-release.ps1` — это maintainer-тулинг (local-only, в `.git/info/exclude`,
в публичный репозиторий не уезжает). Он делает релиз целиком одной командой и сам же
не даёт уехать следам автоматизации.

Полная публикация (подготовка + проверки + git + GitHub):

```powershell
./prepare-release.ps1 -Version 0.3.2 -Publish
```

Будет запрошено подтверждение (ввести тег `v0.3.2`). Чтобы пропустить вопрос — `-Yes`.

Только локальная подготовка, без публикации (как раньше):

```powershell
./prepare-release.ps1 -Version 0.3.2
```

Посмотреть, что произойдёт, ничего не меняя и не публикуя:

```powershell
./prepare-release.ps1 -Version 0.3.2 -Publish -DryRun
```

Перепаковать уже собранный бинарник, не пересобирая:

```powershell
./prepare-release.ps1 -Version 0.3.2 -NoBuild
```

Что делает скрипт по шагам:

1. обновляет `appVersion` в `main.go` и version pointers в Markdown (баннер, `**vX.Y.Z**`);
2. переносит секции `CHANGELOG.md` из `Unreleased` в указанную версию (RU и EN), если её ещё нет;
3. создаёт `release-notes-vX.Y.Z.md`, если файла ещё нет;
4. собирает `goip-bridge` под `linux/amd64` — нативным `go`, а если его нет в PATH (Windows), через WSL (`/usr/local/go/bin/go`);
5. пакует `goip-bridge-linux-amd64.tar.gz` (бинарник `0755`, остальное `0644`);
6. пересчитывает `checksums.txt` (SHA256);

в режиме `-Publish` дополнительно:

7. `gofmt` и `go vet` должны быть чистыми;
8. скан AI-отпечатков по tracked-файлам должен быть пуст (имена моделей и вендоров,
   типовые маркеры автоматических коммит-трейлеров) — иначе релиз прерывается;
9. ставит в индекс только релизный allow-list (никакого `git add -A`), коммитит
   сообщением `Release vX.Y.Z` без трейлеров, создаёт аннотированный тег;
10. пушит ветку и тег, затем `gh release create` с прикреплёнными ассетами.

Требования боевого режима: текущая ветка `main`, тег `vX.Y.Z` ещё не существует,
`gh` авторизован, рабочее дерево содержит только осмысленные изменения релиза.
Ассеты (`goip-bridge`, `*.tar.gz`, `checksums.txt`) прикрепляются к GitHub Release,
но не коммитятся (они в `.gitignore`).

### Ручной контроль после скрипта

1. Проверить `CHANGELOG.md` и `release-notes-vX.Y.Z.md`: текст релиза должен быть человеческим, не только шаблонным.
2. Проверить `README.md`, `README.en.md`, `INSTALL.md`, `DOWNLOAD.md`, `CONFIG.md`, `API.md`, `SCHEMES.md`, `MYSQL.md`, `FIREWALL.md`, `TROUBLESHOOTING.md`.
3. Если скрипт запускался с `-NoBuild`, отдельно убедиться, что бинарник свежий.
4. Проверить архив:

```sh
tar -tzvf goip-bridge-linux-amd64.tar.gz | head
```

5. Проверить суммы:

```sh
sha256sum -c checksums.txt
```

На Windows можно проверить через PowerShell:

```powershell
Get-Content checksums.txt
```

6. Проверить, что в release assets есть готовые файлы, а не только `Source code`.
7. В тексте релиза явно написать: "Если вы обычный пользователь, скачайте `goip-bridge` или `goip-bridge-linux-amd64.tar.gz` из Assets".
8. Проверить, что `mysql.schema.sql` соответствует полям, которые реально использует код.
9. Проверить, что скриншоты не содержат серийники, IMEI, публичные IP, токены или другие секреты.

### Старый ручной путь

Если по какой-то причине скрипт недоступен:

1. Обновить `CHANGELOG.md`.
2. Проверить документацию.
3. Собрать бинарник:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o goip-bridge .
```

4. Проверить запуск:

```sh
./goip-bridge -config /tmp/smoke.json -init en && ./goip-bridge -config /tmp/smoke.json
```

5. Собрать архив и `checksums.txt` вручную по образцу из `prepare-release.ps1`.

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
- Config: CONFIG.md
- Schemes: SCHEMES.md
- API: API.md
- MySQL: MYSQL.md
- Firewall: FIREWALL.md
- Troubleshooting: TROUBLESHOOTING.md
```

## Что желательно добавить

- Для будущих релизов можно дополнительно публиковать platform-specific имена assets, например `goip-bridge-linux-amd64`, если появятся сборки под несколько платформ.
- Скриншот настройки SMS Server в GoIP.
- Краткое видео или GIF первого запуска.

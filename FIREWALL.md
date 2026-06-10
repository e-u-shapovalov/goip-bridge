# Firewall, nftables, ufw и сеть для goip-bridge

`goip-bridge` не заработает стабильно, если GoIP не может достучаться до UDP-порта `44444`. Firewall, маршруты и автозагрузка сервисов - часть установки.

Короткая схема:

```mermaid
flowchart LR
    goip[GoIP] -->|UDP 44444| fw[Firewall]
    fw --> bridge[goip-bridge]
    app[CRM / backend] -->|TCP 8080 если нужно| bridge
    bridge -->|TCP 3306 обычно localhost| db[(MySQL / MariaDB)]
```

Больше схем: [SCHEMES.md](SCHEMES.md)

## Какие порты нужны

| Порт | Протокол | Откуда | Куда | Открывать |
|---:|---|---|---|---|
| `44444` | UDP | GoIP | `goip-bridge` | Да, обязательно |
| `8080` | TCP | ваше приложение | HTTP API | Только если API нужен не с localhost |
| `3306` | TCP | `goip-bridge` | MySQL/MariaDB | Обычно только localhost |

По умолчанию HTTP API слушает `127.0.0.1:8080`, поэтому наружу открывать `8080` не нужно.

## nftables

Проверьте конфиг:

```sh
sudo nano /etc/nftables.conf
```

Минимальное правило для GoIP-сети `172.16.172.0/24`:

```nft
udp dport 44444 ip saddr 172.16.172.0/24 accept
```

Если GoIP приходит из другой сети, замените `172.16.172.0/24` на свою сеть или конкретный IP устройства.

Применить:

```sh
sudo nft -f /etc/nftables.conf
```

Включить после ребута:

```sh
sudo systemctl enable --now nftables
```

Проверить:

```sh
sudo nft list ruleset | grep 44444
sudo systemctl is-enabled nftables
sudo systemctl status nftables
```

## ufw

Открыть UDP `44444` только для сети GoIP:

```sh
sudo ufw allow from 172.16.172.0/24 to any port 44444 proto udp
```

Если нужно открыть HTTP API для внутренней сети:

```sh
sudo ufw allow from 10.0.0.0/8 to any port 8080 proto tcp
```

Проверка:

```sh
sudo ufw status verbose
```

## firewalld

```sh
sudo firewall-cmd --permanent --add-port=44444/udp
sudo firewall-cmd --reload
sudo firewall-cmd --list-ports
```

## HTTP API наружу

Если API нужен с другой машины, в `config.json`:

```json
"listen_http": "0.0.0.0:8080"
```

После этого открывайте TCP `8080` только для нужного IP или внутренней сети. Не открывайте API на весь интернет без VPN/reverse proxy и сильного `http_token`.

## MySQL/MariaDB

Если MySQL/MariaDB стоит на том же сервере, порт `3306` наружу открывать не нужно.

Если база на отдельном сервере, открывайте `3306/tcp` только от IP сервера с `goip-bridge`.

## Серая сеть GoIP и маршрут

Если GoIP находится в отдельной серой сети, адрес и маршрут должны быть персистентными, а не добавленными руками после каждого ребута.

Пример для Debian/Ubuntu `ifupdown`:

```text
up ip addr add 172.16.172.3/24 dev ens192
up ip route replace 10.0.0.0/8 via 172.16.172.1
```

Здесь:

- `172.16.172.3/24` - адрес сервера, который указывается в GoIP как `SMS Server IP`;
- `172.16.172.1` - gateway в серой сети;
- `10.0.0.0/8` - сеть, куда нужен маршрут;
- `ens192` - имя сетевого интерфейса.

Проверка после ребута:

```sh
ip addr show ens192
ip route
```

## systemd порядок загрузки

`goip-bridge.service` должен стартовать после сети, firewall и базы:

```ini
After=network-online.target nftables.service mariadb.service mysql.service
Wants=network-online.target
```

Включить автозагрузку:

```sh
sudo systemctl enable --now nftables
sudo systemctl enable --now mariadb
sudo systemctl enable --now goip-bridge
```

Если сервис базы называется `mysql`, используйте:

```sh
sudo systemctl enable --now mysql
```

## Проверка после ребута

```sh
ip addr
ip route
sudo nft list ruleset | grep 44444
sudo systemctl is-enabled nftables
sudo systemctl is-enabled mariadb
sudo systemctl is-enabled goip-bridge
sudo systemctl status goip-bridge
```

Проверить, что bridge слушает:

```sh
sudo ss -lunp | grep 44444
sudo ss -ltnp | grep 8080
```

Проверить API:

```sh
curl -i -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

## Частые ошибки

- Открыли `44444/tcp`, а нужен `44444/udp`.
- Добавили правило `nft` вручную, но не сохранили в `/etc/nftables.conf`.
- Не включили `nftables.service`, и после ребута firewall другой.
- В GoIP указан IP, который устройство не видит.
- Забыли маршрут до серой сети GoIP.
- Открыли `8080` или `3306` на весь интернет.

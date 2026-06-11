# goip-bridge - GoIP SMS/USSD API, webhooks and optional MySQL queue

## Quick Start

The commands below target Linux x86-64 / amd64. Switch to root with `sudo -i` or prefix system commands with `sudo`.

Create a directory and download the latest release:

```sh
sudo install -d -m 755 /opt/goip-bridge
cd /opt/goip-bridge
sudo curl -L -o goip-bridge https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
sudo chmod +x goip-bridge
```

Run it once:

```sh
sudo ./goip-bridge
```

Choose `ru` or `en`. The bridge creates `config.json` and exits. Fill the config:

```sh
sudo nano config.json
```

At minimum, change `http_token`. If you want webhooks, set `webhook_url`. If you pin a line password in `line_passwords`, use the same password in GoIP.

Start the bridge:

```sh
sudo ./goip-bridge -config config.json
```

Open the GoIP SMS settings:

```text
http://goip8/default/en_US/config.html?type=sms
```

If your gateway is not available as `goip8`, use your GoIP IP address or host name. Fill in:

```text
SMS Server: Enable
SMS Server IP: IP address of your Linux server
SMS Server Port: 44444
SMS Client ID: Go1
Password: password for this channel
```

The default GoIP port is UDP `44444`, five fours. Do not confuse it with `4444`. Save the settings and reboot the gateway.

GoIP window example:

![GoIP SMS Server settings](docs/screenshots/goip-sms-server-settings.png)

Check locally that the line registered:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Check firewall rules and listening ports:

```sh
sudo nft list ruleset | grep -E '8080|44444'   # nftables
sudo ufw status verbose                        # ufw
sudo firewall-cmd --list-ports                 # firewalld
sudo ss -lntup | grep -E ':(8080|44444)\b'     # who is listening
```

You need UDP `44444` from GoIP to the server. Open TCP `8080` only when the HTTP API must be reached from another machine. Use your firewall/distribution documentation for the exact allow commands.

Check USSD, for example balance. Replace `*100#` with your mobile operator's code:

```sh
curl -X POST http://127.0.0.1:8080/ussd \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","code":"*100#"}'
```

Without MySQL, the reply is returned directly by curl. In MySQL mode, the result also arrives as a webhook event with `type:"done"` when `webhook_url` is set.

Send the first SMS:

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"+996700000001","text":"Test message"}'
```

Receive the first inbound SMS: send an SMS to the SIM card inside GoIP and check your webhook or the local inbox:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/inbox
```

Congratulations, the minimal setup is done.

### Run as a systemd service

Short example for Debian/systemd x86_64, for example:

```text
Linux modern 6.12.74+deb13+1-amd64 #1 SMP PREEMPT_DYNAMIC Debian 6.12.74-2 (2026-03-08) x86_64 GNU/Linux
```

The unit file expects the binary and config in `/opt/goip-bridge`:

```sh
sudo useradd --system --home /opt/goip-bridge --shell /usr/sbin/nologin goip-bridge
sudo chown -R goip-bridge:goip-bridge /opt/goip-bridge
sudo curl -L -o /etc/systemd/system/goip-bridge.service https://raw.githubusercontent.com/e-u-shapovalov/goip-bridge/main/goip-bridge.service
sudo systemctl daemon-reload
sudo systemctl enable --now goip-bridge
sudo systemctl status goip-bridge
sudo journalctl -u goip-bridge -f
```

**goip-bridge** is a lightweight server-side gateway for **GoIP DBL / Hybertone** GSM devices. It turns the GoIP SMS Server UDP protocol into a practical HTTP API, incoming SMS webhooks, USSD requests and an optional MySQL inbox/outbox queue.

Run one binary on a Linux server, point the GoIP channel's **SMS Server IP/Port** to it, and integrate SMS with your CRM, bot, billing system, monitoring stack or backend service.

Main Russian README: [README.md](README.md)

Visual schemes and first-run maps: [SCHEMES.md](SCHEMES.md)

## Download

For normal users, no Git and no source build are required.

1. Open GitHub Releases: <https://github.com/e-u-shapovalov/goip-bridge/releases>
2. Open the latest release, currently **v0.3.2**.
3. Download the **`goip-bridge`** file from **Assets**.
4. Do not download `Source code (zip)` or `Source code (tar.gz)` unless you want to build from source.
5. Do not use `Code -> Download ZIP` if you only need the ready-to-run program.

Direct Linux x86-64 / amd64 binary:

<https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge>

Beginner download guide: [DOWNLOAD.md](DOWNLOAD.md)

## Features

- Registers GoIP lines via the UDP SMS Server protocol, default port `44444`.
- Lists live lines via `GET /lines`.
- Receives inbound SMS from GoIP.
- Keeps the latest 500 inbound messages in memory via `GET /inbox`.
- Sends inbound SMS and DLR events to an outgoing webhook.
- Retries webhook delivery in memory with exponential backoff and writes
  undelivered events to `goip-bridge.fallback.jsonl`.
- Sends SMS via `POST /sms`.
- Runs USSD commands via `POST /ussd`.
- Tracks asynchronous queue jobs via `GET /status/{id}`.
- Cancels still-queued jobs via `DELETE /message/{id}`.
- Exposes token-free `GET /health` for local monitoring.
- Protects the HTTP API with a bearer token.
- Optional MySQL integration:
  - inbound SMS are inserted into `goip_inbox`;
  - outbound SMS are read from `goip_outbox`;
  - SMS status is written back as `queued`, `sending`, `sent`, `delivered`,
    `failed` or `cancelled`;
  - USSD results use `done` and `reply`.
- Restricts device UDP sources with `allow_src`.
- Controls queue pacing with `send_pacing` and default queue routing with
  `default_lines`.
- Single Linux amd64 binary.

## Why use it

Many GoIP integrations end up with manual web UI checks, old `goipcron` setups, Apache/PHP scripts and fragile database glue. `goip-bridge` gives you a smaller architecture:

```text
GoIP DBL / Hybertone -> goip-bridge -> HTTP API / webhook / MySQL -> your app
```

Use it for lawful messaging only and with recipient consent.

## Quick Start Without MySQL

```sh
mkdir -p /opt/goip-bridge
cd /opt/goip-bridge
curl -L -o goip-bridge https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
chmod +x goip-bridge
```

Create `config.json`:

```sh
./goip-bridge -config config.json -init en
```

This creates an annotated JSONC config. Use `-init ru` for Russian comments.
Minimal no-MySQL config:

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

Run:

```sh
./goip-bridge -config config.json
```

In the GoIP web interface, set the channel's **SMS Server IP** to the Linux server running `goip-bridge` and **SMS Server Port** to `44444`.

Check registered lines:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

Detailed installation guide: [INSTALL.md](INSTALL.md)

Full configuration reference: [CONFIG.md](CONFIG.md)

Firewall note: the GoIP device must be able to reach UDP port `44444` on the bridge server. Keep HTTP `8080` private unless another host needs the API. Details: [FIREWALL.md](FIREWALL.md)

For synchronous `/sms`, do not rely on HTTP `200` alone. Device-level send
failures are returned as JSON with `status: "failed"` and an `error` field. If
`line` is empty without MySQL, the bridge picks the alive line with the lowest
id. In MySQL queue mode, empty `line` is routed round-robin through
`default_lines` or all alive lines.

## HTTP API

Use `Authorization: Bearer <http_token>` when `http_token` is configured.

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/lines
```

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"996700000001","text":"Test message"}'
```

```sh
curl -X POST http://127.0.0.1:8080/ussd \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","code":"*100#"}'
```

If MySQL is not configured, `/sms` and `/ussd` are synchronous. If MySQL is
configured, both endpoints are asynchronous and return HTTP `202` with an `id`;
use `GET /status/{id}`, webhook events or SQL to see the final result.

Full API reference: [API.md](API.md)

## MySQL Mode

MySQL is optional. If the `db` section is absent from `config.json`, the bridge works with HTTP API, webhooks and in-memory inbox only.

When MySQL is enabled, inbound SMS are inserted into `goip_inbox`, outbound messages are read from `goip_outbox`, and delivery status is written back to the same table.

Schema and examples: [MYSQL.md](MYSQL.md)

Current MySQL runtime details: up to 8 open DB connections, one active SMS/USSD
job per line, per-line delay controlled by `send_pacing`, queue pages of 100
rows, and DLR matching retry up to 6 attempts with 1.5 seconds between attempts.

## Developer Build

Requires Go **1.24** or newer.

```sh
git clone https://github.com/e-u-shapovalov/goip-bridge.git
cd goip-bridge
go run . -config config.json -init en   # creates an annotated config.json (or -init ru)
# edit config.json, then run:
go run . -config config.json
```

Static Linux amd64 build:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o goip-bridge .
```

## Troubleshooting

- `unauthorized`: wrong bearer token.
- Empty `/lines`: GoIP is not reaching UDP port `44444`.
- `no alive line`: no registered GoIP line is currently alive.
- `ussd timeout`: the device or mobile operator did not answer in time.
- `queue temporarily unavailable (db reconnecting)`: the `db` section is configured but MySQL is disconnected; `/sms` and `/ussd` return `503` until reconnect, while `/lines` and `/health` still work.

Full troubleshooting guide: [TROUBLESHOOTING.md](TROUBLESHOOTING.md)

Firewall, routes and boot-time checks: [FIREWALL.md](FIREWALL.md)

## Limitations

- Requires a GoIP / DBL / Hybertone device with SMS Server support.
- This is not an SMPP server.
- `/inbox` is in-memory and stores the latest 500 messages only.
- Persistent inbound SMS storage requires MySQL mode.
- Test your own GoIP model, firmware, SIM cards and carrier before production use.

## License and Support

This project is distributed under the MIT License: [LICENSE](LICENSE).

Repository: <https://github.com/e-u-shapovalov/goip-bridge>

Use GitHub Issues for bugs and feature requests.

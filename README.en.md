# goip-bridge - GoIP SMS/USSD API, webhooks and optional MySQL queue

**goip-bridge** is a lightweight server-side gateway for **GoIP DBL / Hybertone** GSM devices. It turns the GoIP SMS Server UDP protocol into a practical HTTP API, incoming SMS webhooks, USSD requests and an optional MySQL inbox/outbox queue.

Run one binary on a Linux server, point the GoIP channel's **SMS Server IP/Port** to it, and integrate SMS with your CRM, bot, billing system, monitoring stack or backend service.

Main Russian README: [README.md](README.md)

Visual schemes and first-run maps: [SCHEMES.md](SCHEMES.md)

## Download

For normal users, no Git and no source build are required.

1. Open GitHub Releases: <https://github.com/e-u-shapovalov/goip-bridge/releases>
2. Open the latest release, currently **v0.3.1**.
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
- Sends SMS via `POST /sms`.
- Runs USSD commands via `POST /ussd`.
- Protects the HTTP API with a bearer token.
- Optional MySQL integration:
  - inbound SMS are inserted into `goip_inbox`;
  - outbound SMS are read from `goip_outbox`;
  - delivery status is written back as `sent`, `delivered` or `failed`.
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

Firewall note: the GoIP device must be able to reach UDP port `44444` on the bridge server. Keep HTTP `8080` private unless another host needs the API. Details: [FIREWALL.md](FIREWALL.md)

For `/sms`, do not rely on HTTP `200` alone. Device-level send failures are returned as JSON with `status: "failed"` and an `error` field. If `line` is empty, the bridge picks one live line with no guaranteed order; specify `line` for deterministic routing.

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

Full API reference: [API.md](API.md)

## MySQL Mode

MySQL is optional. If the `db` section is absent from `config.json`, the bridge works with HTTP API, webhooks and in-memory inbox only.

When MySQL is enabled, inbound SMS are inserted into `goip_inbox`, outbound messages are read from `goip_outbox`, and delivery status is written back to the same table.

Schema and examples: [MYSQL.md](MYSQL.md)

Current MySQL runtime limits: up to 8 open DB connections, up to 8 concurrent SMS sends, DLR matching retry up to 6 attempts with 1.5 seconds between attempts.

## Developer Build

Requires Go **1.24** or newer.

```sh
git clone https://github.com/e-u-shapovalov/goip-bridge.git
cd goip-bridge
cp config.no-mysql.example.json config.json
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
- `WARNING: MySQL connect failed, retrying in background`: the `db` section is configured but the connection failed; the bridge retries every 15 seconds and the HTTP API keeps working.

Full troubleshooting guide: [TROUBLESHOOTING.md](TROUBLESHOOTING.md)

Firewall, routes and boot-time checks: [FIREWALL.md](FIREWALL.md)

## Limitations

- Requires a GoIP / DBL / Hybertone device with SMS Server support.
- This is not an SMPP server.
- `/inbox` is in-memory and stores the latest 500 messages only.
- Persistent inbound SMS storage requires MySQL mode.
- Test your own GoIP model, firmware, SIM cards and carrier before production use.

## License and Support

No `LICENSE` file was found in this local copy. Add one before serious public distribution.

Repository: <https://github.com/e-u-shapovalov/goip-bridge>

Use GitHub Issues for bugs and feature requests.

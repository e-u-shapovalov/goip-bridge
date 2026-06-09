# goip-bridge - GoIP SMS and USSD API without MySQL, Apache or goipcron

**goip-bridge** is a lightweight standalone gateway for **GoIP DBL/Hybertone** GSM gateways. It connects a physical GoIP device to a simple HTTP API: receive SMS, send SMS, run USSD commands and forward inbound events to a webhook.

In practice, you run one binary on a Linux server, configure it as the **SMS Server** in the GoIP web interface, and use JSON over HTTP from your CRM, bot, billing system, monitoring tool or backend service.

Main Russian README: [README.md](README.md)

## Download

If you just want to run the program, **do not use `Code -> Download ZIP`**. That downloads the source code, not the ready-to-run program.

Download a release build instead:

1. Open the GitHub project page.
2. Find **Releases** on the right side.
3. Open **Latest** or a specific version, for example `v0.1.0`.
4. Scroll to **Assets**.
5. Download a file named like `goip-bridge`, `goip-bridge-linux-amd64` or `goip-bridge-linux-amd64.tar.gz`.
6. Do not download `Source code (zip)` or `Source code (tar.gz)` unless you are a developer.

Beginner-friendly guide: [DOWNLOAD.md](DOWNLOAD.md)

If there is no `goip-bridge` binary under release assets, the release has not been published with a ready build yet.

## Features

- Registers GoIP lines via UDP keepalive, default port `44444`.
- Lists active lines via `GET /lines`.
- Receives inbound SMS from GoIP.
- Stores the latest 500 inbound messages in memory.
- Sends inbound SMS and delivery reports to an outgoing webhook.
- Sends SMS via `POST /sms`.
- Runs USSD commands via `POST /ussd`.
- Protects the HTTP API with a bearer token.
- Works without MySQL, Apache, PHP or external services.
- Builds into a single static binary.

## Why use it

Without a bridge, GoIP integrations often rely on the old goipcron stack, a database, web server setup and custom scripts. `goip-bridge` keeps the architecture much smaller:

`GoIP -> goip-bridge -> HTTP API / webhook -> your application`

It is useful when you need a GoIP SMS gateway, a GoIP HTTP API, incoming SMS webhooks, USSD balance checks or a simpler replacement for goipcron.

Use it only for lawful messaging scenarios and with recipient consent.

## Quick start on Linux

The prepared release target is **Linux x86-64 / amd64**.

```sh
chmod +x goip-bridge
./goip-bridge -config config.json
```

Example `config.json`:

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

In the GoIP web interface, set the channel's **SMS Server IP/Port** to the server running `goip-bridge` and UDP port `44444`.

Check registered lines:

```sh
curl -H "Authorization: Bearer CHANGE_ME" http://127.0.0.1:8080/lines
```

Detailed setup: [INSTALL.md](INSTALL.md)

## HTTP API

Use `Authorization: Bearer <http_token>` when `http_token` is configured.

```sh
curl -H "Authorization: Bearer CHANGE_ME" http://127.0.0.1:8080/lines
```

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"996700000001","text":"Test message"}'
```

```sh
curl -X POST http://127.0.0.1:8080/ussd \
  -H "Authorization: Bearer CHANGE_ME" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","code":"*100#"}'
```

```sh
curl -H "Authorization: Bearer CHANGE_ME" http://127.0.0.1:8080/inbox
```

## Developer build

Requires Go 1.21 or newer.

```sh
git clone https://github.com/e-u-shapovalov/goip-bridge.git
cd goip-bridge
cp config.example.json config.json
go run . -config config.json
```

Static Linux build:

```sh
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go build -o goip-bridge .
```

## Limitations

- Requires a GoIP/DBL/Hybertone device that supports the UDP **SMS Server** protocol.
- This is not an SMPP server. It provides HTTP API and webhooks on top of GoIP.
- `/inbox` is in-memory only and keeps the latest 500 inbound messages.
- Long SMS splitting/reassembly is handled by the GoIP device.
- Version `0.1.0` is an early release. Test it with your own GoIP model and network before production use.

## License and support

No license file was found in the local repository copy. Add a `LICENSE` file before public distribution.

For bugs and feature requests, use GitHub Issues.

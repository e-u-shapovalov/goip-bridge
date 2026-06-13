# goip-bridge - GoIP SMS/USSD API, webhooks and optional MySQL queue

> Already running? Everyday commands (update, status, send, reset the queue) are in the [**Command Cheatsheet**](#command-cheatsheet).

## Quick Start

The commands below target Linux x86-64 / amd64. Switch to root with `sudo -i` or prefix system commands with `sudo`.

Create a directory and download the latest release:

```sh
sudo mkdir -p /opt/goip-bridge
cd /opt/goip-bridge
sudo curl -L -o goip-bridge https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
sudo chmod +x goip-bridge
```

Run it once:

```sh
sudo ./goip-bridge
```

Choose `ru` or `en`. The bridge creates `config.json` and exits.

The screenshot below shows the download and first run: the bridge prints a version banner, asks for the language, creates `config.json` and hints what to fill in.

![goip-bridge first run](docs/screenshots/first-run.png)

Fill the config:

```sh
sudo nano config.json
```

At minimum, change `http_token`. If you want webhooks, set `webhook_url`. If you pin a line password in `line_passwords`, use the same password in GoIP.

Start the bridge:

```sh
sudo ./goip-bridge -config config.json
```

At startup the bridge prints a version banner and a `config in effect` table - the settings that actually took effect. Secrets (`http_token`, `webhook_token`, `webhook_url`) are shown as `set`, not their value, so the log is safe to share. The last lines are `listening on UDP :44444` and `HTTP API on 127.0.0.1:8080`.

![goip-bridge startup log and config in effect](docs/screenshots/startup-config-in-effect.png)

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

Check the firewall and listening ports. Run the command for your distribution - the other firewalls may not be installed, and `command not found` is normal.

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

Who is actually listening on the ports (any distribution):

```sh
sudo ss -lntup | grep -E ':(8080|44444)\b'
```

You need UDP `44444` from GoIP to the server. Open TCP `8080` only when the HTTP API must be reached from another machine. Use your firewall/distribution documentation for the exact allow commands.

Check USSD, for example balance. Replace `*100#` with your mobile operator's code:

```sh
curl -X POST http://127.0.0.1:8080/ussd \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","code":"*100#"}'
```

The reply is returned directly by curl. When `webhook_url` is set, the result is also duplicated as a webhook event with `type:"done"` in every mode - both without MySQL and with the queue; in MySQL mode it is preceded by `type:"queued"`.

Send the first SMS:

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"+996700000001","text":"Test message"}'
```

The number format depends on the SIM card's operator. The bridge accepts a number with or without `+` (`+996700000001` or `996700000001`) - the check is simple: an optional `+` and 3-20 digits. After that the number is handled by GoIP and the operator network, and their requirements differ: some accept the international format with `+`, some without `+`, and some reject their own country code and expect the local format (for example `0700000001`). If an SMS ends up `failed` or does not arrive, try a different number format for that operator.

Receive the first inbound SMS: send an SMS to the SIM card inside GoIP and check your webhook or the local inbox:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" http://127.0.0.1:8080/inbox
```

`/inbox` shows the latest 500 inbound SMS from the current process memory, not from a database. This list is cleared when the bridge restarts. For persistent storage, enable MySQL - then inbound SMS are written to the `goip_inbox` table and survive a restart.

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

### Managing the service and logs

```sh
sudo systemctl status goip-bridge    # state: active/failed, PID, last log lines
sudo systemctl restart goip-bridge   # restart (e.g. after editing config.json)
sudo systemctl stop goip-bridge      # stop
sudo systemctl start goip-bridge     # start
```

The systemd journal:

```sh
sudo journalctl -u goip-bridge -f                  # live tail (Ctrl+C to exit)
sudo journalctl -u goip-bridge -n 100 --no-pager   # last 100 lines
sudo journalctl -u goip-bridge --since "10 min ago" --no-pager
sudo journalctl -u goip-bridge --since today | grep -i webhook
```

File logs - the bridge also writes them next to the config (mode `0600`, so use root):

```sh
sudo tail -f /opt/goip-bridge/goip-bridge.log       # same content as journalctl
sudo tail -50 /opt/goip-bridge/goip-bridge.err.log  # errors and WARN only
```

The content is the same - use whichever is convenient. `.err.log` is the signal file: only problems land there (`webhook OK` is not written to it), so start with it when something is broken. `goip-bridge.log.prev` is the previous run's log (see `clear_logs_on_start`). Verbose per-SMS/USSD logging is enabled with `"debug": true`.

### Updating the version

On an update only the binary changes; `config.json` stays in place.

The shortest path is the built-in self-update. The command downloads the latest release and `checksums.txt`, verifies the SHA256, backs the current binary up to `.bak`, swaps the binary atomically and deletes the `.bak` on success (the `.bak` is kept only when the update failed - for rollback):

```sh
sudo -u goip-bridge /opt/goip-bridge/goip-bridge -update
sudo systemctl restart goip-bridge
```

When `-update` is run as root, the systemd service restart happens automatically.

The manual way - download the new version next to the old one, swap it atomically and restart the service:

```sh
sudo curl -L -o /opt/goip-bridge/goip-bridge.new https://github.com/e-u-shapovalov/goip-bridge/releases/latest/download/goip-bridge
sudo chmod +x /opt/goip-bridge/goip-bridge.new
sudo chown goip-bridge:goip-bridge /opt/goip-bridge/goip-bridge.new
sudo mv /opt/goip-bridge/goip-bridge.new /opt/goip-bridge/goip-bridge
sudo systemctl restart goip-bridge
sudo journalctl -u goip-bridge -n 20 --no-pager
```

You can also learn about new versions automatically: enable `"check_updates": true` in `config.json` - on startup the bridge asks GitHub in the background and, when a newer release exists, prints a prominent box with its number. The check is off by default (the bridge never "phones home"), and the log says so in one line.

After the restart the first log line is the banner with the new version. Check the version without logs:

```sh
/opt/goip-bridge/goip-bridge -version
```

What matters on an update:

- `config.json` is backward compatible: new versions do not break the old config, new fields fall back to defaults. Change the config only to enable a new feature - see the release notes.
- Do not repeat the install commands (`useradd`, `daemon-reload`, `enable`) - only swap the binary and `restart`.
- Update the unit file only when the release notes say it changed.

### Creating and connecting MySQL

Without MySQL the bridge works through the HTTP API and webhooks. MySQL is needed if your app wants an outbound SMS queue in a table and persistent storage of inbound SMS.

The ready binary does not include the schema file - download it from the repository:

```sh
cd /opt/goip-bridge
sudo curl -L -o mysql.schema.sql https://raw.githubusercontent.com/e-u-shapovalov/goip-bridge/main/mysql.schema.sql
```

Create the database, user and tables:

```sh
sudo mysql < mysql.schema.sql
```

If you get `ERROR 1045 (28000): Access denied for user 'root'`, the MySQL root user has a password (passwordless socket auth is not available). Run it with a password prompt, or apply the schema as any MySQL admin user, for example via phpMyAdmin:

```sh
mysql -u root -p < mysql.schema.sql
```

This creates the `goip_go` database, the `goip_bridge@127.0.0.1` user and the `goip_inbox` and `goip_outbox` tables.

The schema gives the user a placeholder password `CHANGE_ME_STRONG_DB_PASSWORD`. Replace it with your own:

```sql
ALTER USER 'goip_bridge'@'127.0.0.1' IDENTIFIED BY 'STRONG_PASSWORD';
FLUSH PRIVILEGES;
```

Add the `db` block to `config.json` (if the config was created with `-init`, uncomment the ready block) and use the same password:

```json
"db": {
  "host": "127.0.0.1",
  "port": 3306,
  "user": "goip_bridge",
  "password": "STRONG_PASSWORD",
  "name": "goip_go",
  "inbox_table": "goip_inbox",
  "outbox_table": "goip_outbox",
  "poll_sec": 3
}
```

Restart the service:

```sh
sudo systemctl restart goip-bridge
```

Check that the database connected - the log shows a line like `db: connected to goip_bridge@127.0.0.1:3306/goip_go — inbox table ... + outbox queue ... active`:

```sh
sudo journalctl -u goip-bridge -n 20 --no-pager
```

If instead you see `db: configured but NOT connected ... retrying in background`, check the password, the user's privileges and that MySQL is reachable. While the database is down, `/sms` and `/ussd` in queue mode return `503`. More: [MYSQL.md](MYSQL.md) and [TROUBLESHOOTING.md](TROUBLESHOOTING.md).

Full table schema, user privileges and `INSERT` examples: [MYSQL.md](MYSQL.md).

**goip-bridge** is a lightweight server-side gateway for **GoIP DBL / Hybertone** GSM devices. It turns the GoIP SMS Server UDP protocol into a practical HTTP API, incoming SMS webhooks, USSD requests and an optional MySQL inbox/outbox queue.

Run one binary on a Linux server, point the GoIP channel's **SMS Server IP/Port** to it, and integrate SMS with your CRM, bot, billing system, monitoring stack or backend service.

Main Russian README: [README.md](README.md)

Visual schemes and first-run maps: [SCHEMES.md](SCHEMES.md)

## Download

For normal users, no Git and no source build are required.

1. Open GitHub Releases: <https://github.com/e-u-shapovalov/goip-bridge/releases>
2. Open the latest release, currently **v0.5.0**.
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
- Sends send-result webhook events (`queued`, `sent`, `done`, `failed`) in
  every mode - with the MySQL queue and without it.
- Monitors lines via webhook: `line_down`/`line_up` when keepalive disappears
  and recovers, `line_failing`/`line_recovered` on a streak of send failures
  (threshold `fail_threshold`).
- Retries webhook delivery in memory with exponential backoff and writes
  undelivered events to `goip-bridge.fallback.jsonl`. Redirects are not
  followed (3xx = delivery failure) and every delivery's HTTP status is logged.
- Self-updates with one command: `goip-bridge -update` (SHA256-verified, with a
  `.bak` rollback on failure) plus an optional startup version check
  (`check_updates`, off by default).
- Keeps the log folder clean: on startup the previous run's logs are moved to
  one `.prev` copy each (`clear_logs_on_start`, on by default).
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
`line` is empty, the bridge picks an alive line round-robin — through
`default_lines` or all alive lines — in both the synchronous and the MySQL
queue mode.

## Command Cheatsheet

What the bridge can do and how to ask - one line each. Detail: HTTP - [API.md](API.md), database - [MYSQL.md](MYSQL.md), webhook events - [below](#webhook-events).

> Every HTTP request except `/health` needs `Authorization: Bearer <http_token>`. The API listens on `127.0.0.1:8080` by default. Token on the server: `grep -oP '"http_token"\s*:\s*"\K[^"]+' /opt/goip-bridge/config.json`

<details>
<summary><b>Service control</b> (update, version, health)</summary>

| Task | Command | Notes |
|---|---|---|
| Update | `sudo /opt/goip-bridge/goip-bridge -update` | downloads the latest release, verifies SHA256, swaps the binary, restarts (as root) |
| Version | `/opt/goip-bridge/goip-bridge -version` | prints the running version |
| Health | `curl http://127.0.0.1:8080/health` | `{ok, lines, alive, db}` - no token |

</details>

<details>
<summary><b>Via HTTP request</b></summary>

| Task | Request | Notes |
|---|---|---|
| Status | `POST /stats` | version, uptime, RAM, lines, queue - reply also in `/inbox` + webhook |
| Reset | `POST /reset` | cancel all `queued` + flush caches, no restart (cancels the whole queue!) |
| Send SMS | `POST /sms` `{"line":"Go1","to":"+996700000001","text":"Hello"}` | with MySQL - async (HTTP 202), otherwise result in the response |
| USSD | `POST /ussd` `{"line":"Go1","code":"*100#"}` | operator reply in the response / `reply` column |
| Lines | `GET /lines` | all lines: alive, signal, carrier, number |
| Inbox | `GET /inbox` | last 500, in memory |
| Message status | `GET /status/<id>` | status, queue position, channel health |
| Cancel message | `DELETE /message/<id>` | if still `queued` → `cancelled`, else 409 |

Full example: `curl -X POST http://127.0.0.1:8080/stats -H "Authorization: Bearer <http_token>"`

</details>

<details>
<summary><b>Via the database (MySQL queue)</b></summary>

| Task | SQL | Notes |
|---|---|---|
| Status | `INSERT INTO goip_outbox (type,to_number,status) VALUES ('cmd','status','queued');` | reply arrives in `goip_inbox` (`line='system'`) and the webhook |
| Reset | `INSERT INTO goip_outbox (type,to_number,status) VALUES ('cmd','reset','queued');` | cancel all `queued` + flush caches (cancels the whole queue!) |
| Send SMS | `INSERT INTO goip_outbox (type,line,to_number,text,status) VALUES ('sms','Go1','+996700000001','Hello','queued');` | empty/NULL `line` = any alive (round-robin) |
| USSD | `INSERT INTO goip_outbox (type,line,to_number,status) VALUES ('ussd','Go1','*100#','queued');` | reply in the `reply` column when `status='done'` |
| Inbox | `SELECT id,line,from_number,text,received_at FROM goip_inbox ORDER BY id DESC LIMIT 20;` | persistent storage |
| Command reply | `SELECT text FROM goip_inbox WHERE line='system' ORDER BY id DESC LIMIT 1;` | JSON reply to status/reset |

</details>

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

## Webhook Events

The rule is simple: **if `webhook_url` is set - events are always sent**, in every mode, with MySQL and without it. An empty `webhook_url` = webhook off.

| Event | When it arrives |
|---|---|
| `sms` | inbound SMS from a line |
| `dlr` | delivery report from GoIP |
| `queued` | a message entered the queue - both via HTTP and via a direct `INSERT` into `goip_outbox` |
| `sent` / `failed` | SMS send result |
| `done` / `failed` | USSD result - the operator's answer in the `reply` field |
| `line_down` / `line_up` | a line disappeared / recovered (by keepalive, threshold `line_dead_after_sec`) |
| `line_failing` / `line_recovered` | `fail_threshold` consecutive send failures / success again |
| `stat` | reply to a `status`/`reset` command (the same body is written into `goip_inbox`, `line='system'`) |

The `failed`/`dlr` events carry a human-readable description next to the code: `error_desc` (for DLR - `state_desc`), e.g. `errorstatus:38` → `Network out of order`.

USSD has no "inbound" direction: it is always request-response. The request goes via `/ussd` or an `INSERT` with `type='ussd'`, the operator's answer arrives as a `done` event (in MySQL mode also in the `reply` column).

`line_down`/`line_up` fire on state transitions: on startup the bridge memorizes the current line states silently, so a restart does not send a burst of false `line_up` events.

Every delivery is visible in the log as `webhook OK 200`. Redirects are not followed: `webhook WARN 301` in the log means `webhook_url` redirects and must be fixed - see [TROUBLESHOOTING.md](TROUBLESHOOTING.md). The format of all events with JSON examples: [API.md](API.md).

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

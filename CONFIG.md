# Configuration Reference for goip-bridge

This file explains every setting that `goip-bridge` reads from `config.json`.
Use it when you want to understand what each option does, why it exists and
what value is safe for a first installation.

Russian summary is included because many GoIP deployments in this project use
Russian-language operations docs.

## How to Create a Config

Recommended for a new user:

```sh
./goip-bridge -config config.json -init en
```

Russian comments:

```sh
./goip-bridge -config config.json -init ru
```

If `config.json` does not exist and the program runs in an interactive terminal,
`goip-bridge` asks which language to use. The generated file is JSONC: comments
and trailing commas are accepted.

For a small no-MySQL start, copy:

```sh
cp config.no-mysql.example.json config.json
```

Then change at least `http_token`.

## Minimal Config Without MySQL

```json
{
  "listen_udp": ":44444",
  "listen_http": "127.0.0.1:8080",
  "http_token": "CHANGE_ME_TO_LONG_RANDOM_TOKEN",
  "webhook_url": "",
  "webhook_token": "",
  "line_passwords": {}
}
```

Empty or missing fields are filled with defaults by the program.

## Network and GoIP Access

| Setting | Default | What it means |
|---|---:|---|
| `listen_udp` | `:44444` | UDP address where GoIP devices send SMS Server keepalive, inbound SMS and delivery reports. `:44444` means all server interfaces, port 44444. |
| `allow_src` | `[]` | Optional list of allowed GoIP source IPs or CIDR networks. Empty means accept UDP packets from any address that can reach the port. |
| `line_passwords` | `{}` | Optional fixed passwords by line id. Used for sending SMS/USSD and, when a line is listed here, for checking inbound keepalive/SMS/DLR packets. |
| `line_dead_after_sec` | `120` | A line becomes not routable when no keepalive was seen for this many seconds. Affects `/lines`, `/health` and automatic routing. |

Example:

```json
{
  "listen_udp": ":44444",
  "allow_src": ["192.168.1.50", "192.168.1.0/24"],
  "line_passwords": {
    "Go1": "line-secret"
  },
  "line_dead_after_sec": 120
}
```

Why this matters:

- `listen_udp` must match **SMS Server Port** in the GoIP web interface.
- `allow_src` is a simple network safety filter. It does not replace a firewall.
- If `line_passwords` is empty, the bridge learns each password from the device
  keepalive. That is convenient for first start, but pinning passwords is safer
  when the GoIP network is not fully trusted.

Русски: `allow_src` ограничивает, с каких IP bridge вообще принимает UDP-пакеты.
`line_passwords` закрепляет пароль за конкретной линией, например `Go1`.

## HTTP API

| Setting | Default | What it means |
|---|---:|---|
| `listen_http` | `127.0.0.1:8080` | TCP address for `/sms`, `/ussd`, `/lines`, `/inbox`, `/status`, `/message` and `/health`. |
| `http_token` | empty | Bearer token required by all API endpoints except `/health`. Empty means the API is open. |

Safe first value:

```json
{
  "listen_http": "127.0.0.1:8080",
  "http_token": "CHANGE_ME_TO_A_LONG_RANDOM_SECRET"
}
```

If you change `listen_http` to `0.0.0.0:8080`, the API becomes reachable from
other machines. Use a strong `http_token`, firewall rules, VPN or reverse proxy.
The program logs a warning when `http_token` is empty and the API is not bound to
loopback.

## Webhook Delivery

| Setting | Default | What it means |
|---|---:|---|
| `webhook_url` | empty | URL where the bridge sends JSON events. Empty disables webhook delivery. |
| `webhook_token` | empty | Bearer token sent by the bridge to your webhook receiver. |
| `webhook_retry.max_hours` | `3` | Maximum age of a webhook event kept for retry. |
| `webhook_retry.base_sec` | `5` | First retry delay. Later delays double: 5, 10, 20 seconds and so on. |
| `fail_threshold` | `10` | Consecutive send failures on one line that emit `line_failing` and flag the line `suspect` in `/status`. Reset by the first successful send. |

The bridge sends these event types:

- `sms` for inbound SMS.
- `dlr` for delivery reports from GoIP.
- `queued` when a message enters the MySQL queue — both for HTTP `/sms` and
  `/ussd` requests and for rows your application INSERTs into the outbox table
  directly (announced when the bridge claims the row).
- `sent` or `failed` for SMS results.
- `done` or `failed` for USSD results.
- `line_down` when a line stops sending keepalive for longer than
  `line_dead_after_sec`, `line_up` when it recovers.
- `line_failing` after `fail_threshold` consecutive send failures on one line,
  `line_recovered` when a send on that line succeeds again.

When `webhook_url` is set, send-result events (`sent`/`done`/`failed`) fire in
EVERY mode — with the MySQL queue and in the synchronous no-MySQL mode alike.

Webhook queue details from the code:

- events are kept in RAM;
- up to 10,000 pending events are held;
- up to 16 webhook deliveries may be in flight;
- HTTP client timeout is 15 seconds;
- redirects are NOT followed: a `3xx` answer counts as a delivery failure and is
  logged as `webhook WARN 301 ... Location: ...` (a redirect would silently turn
  the POST into a GET and drop the JSON body — point `webhook_url` at the final
  URL);
- every delivery logs its HTTP status (`webhook OK 200` / `webhook WARN ...`);
  on an interactive terminal OK is green and WARN is red, log files always get
  plain text;
- non-2xx responses are retried with exponential backoff;
- expired, dropped or still-pending-on-shutdown events are written to
  `goip-bridge.fallback.jsonl`.

## SMS and USSD Timeouts

| Setting | Default | What it means |
|---|---:|---|
| `send_timeout_sec` | `45` | Maximum time to wait until GoIP confirms an SMS send. Timeout becomes `status: failed`, `error: timeout`. |
| `ussd_timeout_sec` | `120` | Total time to wait for a USSD reply. Timeout becomes `ussd timeout`. |
| `ussd_retransmit_sec` | `60` | While waiting for USSD, resend the same USSD packet after this interval. |

Do not set `ussd_retransmit_sec` too low. Many operators break USSD sessions if
the same request is repeated too frequently.

## MySQL Queue Routing and Pacing

These settings matter mostly when the `db` block is enabled.

| Setting | Default | What it means |
|---|---:|---|
| `send_pacing.default.min_sec` | `3` | Minimum pause after a queue send attempt on one line. |
| `send_pacing.default.max_sec` | `10` | Maximum pause after a queue send attempt on one line. |
| `send_pacing.per_line` | `{}` | Per-line overrides, keyed by GoIP line id. |
| `default_lines` | `[]` | Lines used for queued rows where `line` is `NULL` or empty. Empty means all alive lines. |

Example:

```json
{
  "send_pacing": {
    "default": { "min_sec": 3, "max_sec": 10 },
    "per_line": {
      "Go1": { "min_sec": 5, "max_sec": 5 },
      "Go2": { "min_sec": 1, "max_sec": 40 }
    }
  },
  "default_lines": ["Go1", "Go2"]
}
```

How it works:

- MySQL queue mode sends only one SMS/USSD at a time per line.
- After each real send attempt the line waits for the pacing delay.
- `min_sec == max_sec` gives a fixed pause.
- `max_sec <= 0` disables the pause.
- Rows with a specific `line` wait for that line.
- Rows with no `line` use round-robin over `default_lines`, or over all alive
  lines if `default_lines` is empty.

Direct synchronous HTTP sending without MySQL does not use the queue scheduler,
so `send_pacing` is primarily a MySQL queue control.

## MySQL / MariaDB

| Setting | Default | What it means |
|---|---:|---|
| `db.host` | `127.0.0.1` | MySQL/MariaDB host. |
| `db.port` | `3306` | MySQL/MariaDB TCP port. |
| `db.user` | empty | Database user. |
| `db.password` | empty | Database password. |
| `db.name` | empty | Database name. |
| `db.inbox_table` | `goip_inbox` | Table for inbound SMS. |
| `db.outbox_table` | `goip_outbox` | Table for queued SMS/USSD. |
| `db.poll_sec` | `3` | How often the bridge scans `outbox_table` for `status='queued'`. |

If the `db` block is absent, MySQL is off. `/sms` and `/ussd` are synchronous.

If the `db` block is present, `/sms` and `/ussd` are asynchronous:

- HTTP returns `202` with an `id`;
- the row goes to `goip_outbox`;
- check result through `GET /status/{id}`, webhook events or SQL.

Table names are validated as simple identifiers before use because SQL drivers
cannot parameterize table names.

## Logging and Diagnostics

| Setting | Default | What it means |
|---|---:|---|
| `debug` | `false` | Verbose SMS/USSD/inbound logging. Contains phone numbers and message text. |
| `debug_line` | `false` | One raw keepalive log per line, including line password and identifiers. |
| `log_max_mb` | `10` | Size cap for `goip-bridge.log` and `goip-bridge.err.log`. |
| `clear_logs_on_start` | `true` | On startup move the current bridge logs (`goip-bridge.log`, `.err.log`, `line-*.log`) to one `.prev` copy each. The folder stays clean while the previous run's log — including a crashed one — is preserved. Set `false` to keep appending. |
| `check_updates` | `false` | On startup ask GitHub in the background whether a newer release exists (one GET to `api.github.com`, up to ~3 s, silently skipped when unreachable). When disabled the log gets one line saying the check is off; when enabled and a newer version exists a boxed notice is printed; when current, nothing. Off by default because it contacts an external server. |

Files are written next to the config file:

- `goip-bridge.log`;
- `goip-bridge.err.log`;
- `goip-bridge.line-<id>.log` when `debug_line` is true;
- `*.prev` — the previous run's log, kept by `clear_logs_on_start`;
- `goip-bridge.fallback.jsonl` when fallback records are needed.

Logs and fallback files are created with mode `0600` because they may contain
phone numbers, SMS text, tokens, SIM identifiers or line passwords.

## CLI Flags

| Flag | What it does |
|---|---|
| `-config config.json` | Path to config. Default is `config.json`. |
| `-init ru` | Create an annotated Russian JSONC config at the `-config` path and exit. Refuses to overwrite an existing file. |
| `-init en` | Create an annotated English JSONC config at the `-config` path and exit. |
| `-version` | Print the boxed identity header (name, version, tagline, copyright, repository URL), then exit. |
| `-update` | Download the latest release binary and `checksums.txt` from GitHub, verify the SHA256, back the current binary up to `.bak`, replace it atomically and delete the `.bak` on success (it is kept only when the update failed, for rollback). Restarting the systemd service stays a separate root step (`systemctl restart goip-bridge`) unless `-update` itself runs as root — then it calls systemctl for you. |

## Defaults Applied by Code

If a field is missing or zero, the code applies these defaults:

```text
listen_udp:              :44444
listen_http:             127.0.0.1:8080
send_timeout_sec:        45
ussd_timeout_sec:        120
ussd_retransmit_sec:     60
log_max_mb:              10
line_dead_after_sec:     120
fail_threshold:          10
clear_logs_on_start:     true
check_updates:           false
send_pacing.default:     3..10 seconds
webhook_retry:           max 3 hours, base 5 seconds
db.host:                 127.0.0.1
db.port:                 3306
db.inbox_table:          goip_inbox
db.outbox_table:         goip_outbox
db.poll_sec:             3
```

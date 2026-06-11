# goip-bridge HTTP API

`goip-bridge` gives your CRM, bot, billing panel, monitoring service or backend
a simple HTTP interface for a physical GoIP DBL / Hybertone gateway.

Base URL in examples:

```text
http://127.0.0.1:8080
```

If `http_token` is set in `config.json`, every endpoint except `/health` needs:

```text
Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN
```

## Important Behavior

- `/health` is unauthenticated. All other API endpoints use the bearer token.
- Request bodies for `/sms` and `/ussd` are limited to 1 MB.
- HTTP methods are strict. A wrong method returns `405` and an `Allow` header.
- Phone numbers for `/sms` must match `+` plus digits, or digits only, 3 to 20
  digits total. Spaces, letters and punctuation are rejected.
- Without MySQL, `/sms` and `/ussd` are synchronous: the HTTP response contains
  the device result.
- With MySQL configured, `/sms` and `/ussd` are asynchronous: the HTTP response
  is `202 accepted`, and the result is available through `GET /status/{id}`,
  webhook events and the database row.
- In synchronous `/sms`, device-level send failure still returns HTTP `200` with
  JSON `{"status":"failed"}`. Check the JSON status, not only the HTTP status.

## GET /health

Lightweight monitoring endpoint. It does not require `Authorization`.

```sh
curl http://127.0.0.1:8080/health
```

Example:

```json
{
  "ok": true,
  "lines": 8,
  "alive": 7,
  "db": true
}
```

Fields:

- `lines` - how many GoIP lines have ever registered since process start.
- `alive` - how many lines are routable now: GSM status is `LOGIN` and recent
  keepalive is within `line_dead_after_sec`.
- `db` - whether MySQL is currently connected.

## GET /lines

Returns all GoIP lines known to the current process.

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  http://127.0.0.1:8080/lines
```

Example:

```json
[
  {
    "id": "Go1",
    "addr": "192.168.1.50:12345",
    "num": "996700000001",
    "signal": 25,
    "gsm_status": "LOGIN",
    "imei": "111111111111111",
    "imsi": "222",
    "iccid": "333",
    "carrier": "Operator",
    "alive": true,
    "last_seen": "2026-06-11T10:00:00Z"
  }
]
```

Useful fields:

- `id` - line id from GoIP **SMS Client ID**. Use it as `line` in `/sms`,
  `/ussd` or `goip_outbox.line`.
- `alive` - whether the bridge can route work to this line now.
- `signal`, `gsm_status`, `imei`, `imsi`, `iccid`, `carrier` - values received
  from GoIP keepalive.

## POST /sms Without MySQL

When the `db` block is absent, `/sms` sends directly and waits for the GoIP
send handshake.

```sh
curl -X POST http://127.0.0.1:8080/sms \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","to":"+996700000001","text":"Test message"}'
```

Request:

```json
{
  "line": "Go1",
  "to": "+996700000001",
  "text": "Test message"
}
```

`line` may be empty. In no-MySQL synchronous mode the bridge then selects the
alive line with the lowest id. For predictable routing, pass the line explicitly.

Success:

```json
{
  "line": "Go1",
  "status": "sent",
  "sms_no": "123"
}
```

Device-level failure:

```json
{
  "line": "Go1",
  "status": "failed",
  "error": "timeout"
}
```

That failure response uses HTTP `200` because the API request itself was valid.
Your client must check `status`.

## POST /sms With MySQL

When the `db` block is present and MySQL is connected, `/sms` inserts a row into
`goip_outbox` and returns immediately.

```json
{
  "status": "accepted",
  "id": "1781140000000000-abcdef...",
  "queued_at": 1781140000000000
}
```

Use the returned `id` with:

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  http://127.0.0.1:8080/status/1781140000000000-abcdef
```

If MySQL is configured but temporarily disconnected, `/sms` returns:

```json
{
  "error": "queue temporarily unavailable (db reconnecting)"
}
```

with HTTP `503`. The bridge keeps reconnecting to MySQL in the background.

Routing in queue mode:

- request `line:"Go1"` becomes `goip_outbox.line='Go1'`;
- empty `line` becomes SQL `NULL`;
- SQL `NULL` or empty line is routed by the scheduler using `default_lines`, or
  all alive lines if `default_lines` is empty;
- queue mode keeps one SMS/USSD in flight per line and applies `send_pacing`.

## POST /ussd Without MySQL

```sh
curl -X POST http://127.0.0.1:8080/ussd \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  -H "Content-Type: application/json" \
  -d '{"line":"Go1","code":"*100#"}'
```

Success:

```json
{
  "line": "Go1",
  "reply": "Balance: 42.00"
}
```

Timeout or device error:

```json
{
  "line": "Go1",
  "error": "ussd timeout"
}
```

If `line` is empty in no-MySQL mode, the bridge selects the alive line with the
lowest id.

## POST /ussd With MySQL

With MySQL enabled, `/ussd` is also asynchronous. The USSD code is stored in
`goip_outbox.to_number` with `type='ussd'`.

Accepted response:

```json
{
  "status": "accepted",
  "id": "1781140000000000-abcdef...",
  "queued_at": 1781140000000000
}
```

The final result appears in `GET /status/{id}`, webhook event `done` or `failed`,
and the SQL row fields `status`, `reply` and `error_code`.

## GET /status/{id}

Available only when MySQL is enabled and connected. Returns queue state for the
public `guid` returned by `/sms` or `/ussd`.

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  http://127.0.0.1:8080/status/1781140000000000-abcdef
```

Queued example:

```json
{
  "id": "1781140000000000-abcdef",
  "status": "queued",
  "type": "sms",
  "line": "",
  "to": "+996700000001",
  "text": "Test message",
  "queued_at": "2026-06-11T10:00:00Z",
  "position": 3,
  "before": 2,
  "after": 10
}
```

Sent SMS example:

```json
{
  "id": "1781140000000000-abcdef",
  "status": "sent",
  "type": "sms",
  "line": "Go1",
  "to": "+996700000001",
  "text": "Test message",
  "sms_no": 123,
  "queued_at": "2026-06-11T10:00:00Z",
  "channel": {
    "line": "Go1",
    "alive": true,
    "last_seen_ago_sec": 4,
    "recent_sends": 12,
    "recent_fail_streak": 0,
    "suspect": false
  }
}
```

USSD done example:

```json
{
  "id": "1781140000000000-abcdef",
  "status": "done",
  "type": "ussd",
  "line": "Go1",
  "to": "*100#",
  "reply": "Balance: 42.00",
  "channel": {
    "line": "Go1",
    "alive": true,
    "last_seen_ago_sec": 4,
    "recent_sends": 3,
    "recent_fail_streak": 0,
    "suspect": false
  }
}
```

Errors:

- `400 need id` - missing id after `/status/`.
- `404 no queue (db off)` - MySQL mode is off or not connected.
- `404 not found` - no row with that `guid`.
- `500 db` - database query failed.

## DELETE /message/{id}

Cancels a message only while it is still `queued`.

```sh
curl -X DELETE \
  -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  http://127.0.0.1:8080/message/1781140000000000-abcdef
```

Success:

```json
{
  "id": "1781140000000000-abcdef",
  "status": "cancelled"
}
```

Too late:

```json
{
  "error": "too late",
  "status": "sending"
}
```

Errors:

- `404 no queue (db off)` - MySQL mode is off or not connected.
- `404 not found` - unknown id.
- `409 too late` - row already moved past `queued`.

## GET /inbox

Returns the last 500 inbound SMS kept in process memory.

```sh
curl -H "Authorization: Bearer CHANGE_ME_TO_LONG_RANDOM_TOKEN" \
  http://127.0.0.1:8080/inbox
```

Example:

```json
[
  {
    "line": "Go1",
    "from": "+996555111222",
    "text": "Incoming message",
    "time": "2026-06-11T10:00:00Z"
  }
]
```

Important: this is memory only. It is cleared on process restart. Use MySQL
mode for persistent inbound SMS storage.

## Webhook

If `webhook_url` is set, the bridge sends JSON events to your service:

```json
{
  "webhook_url": "https://example.com/goip-webhook",
  "webhook_token": "WEBHOOK_SECRET",
  "webhook_retry": { "max_hours": 3, "base_sec": 5 }
}
```

Headers:

```text
Content-Type: application/json
Authorization: Bearer WEBHOOK_SECRET
```

The `Authorization` header is sent only when `webhook_token` is not empty.

### Inbound SMS Event

```json
{
  "type": "sms",
  "line": "Go1",
  "from": "+996555111222",
  "text": "Message text",
  "time": "2026-06-11T10:00:00Z"
}
```

The sender number is taken from the first GoIP field found in this order:
`srcnum`, `num`, `src`, `sender`.

### Delivery Report Event

```json
{
  "type": "dlr",
  "line": "Go1",
  "sms_no": "123",
  "state": "0",
  "time": "2026-06-11T10:00:00Z"
}
```

In GoIP protocol, `state: "0"` means delivered. Other values are passed through
and, in MySQL mode, become `failed` with `error_code='dlr_state:<state>'`.

### Queue Events

`queued`:

```json
{
  "type": "queued",
  "id": "1781140000000000-abcdef",
  "msg_type": "sms",
  "line": "Go1",
  "to": "+996700000001",
  "time": "2026-06-11T10:00:00Z",
  "channel": {
    "line": "Go1",
    "alive": true,
    "last_seen_ago_sec": 2,
    "recent_sends": 5,
    "recent_fail_streak": 0,
    "suspect": false
  }
}
```

SMS `sent`:

```json
{
  "type": "sent",
  "id": "1781140000000000-abcdef",
  "msg_type": "sms",
  "line": "Go1",
  "sms_no": "123",
  "channel": {
    "line": "Go1",
    "alive": true,
    "last_seen_ago_sec": 2,
    "recent_sends": 5,
    "recent_fail_streak": 0,
    "suspect": false
  },
  "time": "2026-06-11T10:00:00Z"
}
```

USSD `done`:

```json
{
  "type": "done",
  "id": "1781140000000000-abcdef",
  "msg_type": "ussd",
  "line": "Go1",
  "reply": "Balance: 42.00",
  "channel": {
    "line": "Go1",
    "alive": true,
    "last_seen_ago_sec": 2,
    "recent_sends": 5,
    "recent_fail_streak": 0,
    "suspect": false
  },
  "time": "2026-06-11T10:00:00Z"
}
```

Failure:

```json
{
  "type": "failed",
  "id": "1781140000000000-abcdef",
  "msg_type": "sms",
  "line": "Go1",
  "error": "timeout",
  "channel": {
    "line": "Go1",
    "alive": true,
    "last_seen_ago_sec": 2,
    "recent_sends": 10,
    "recent_fail_streak": 10,
    "suspect": true,
    "suspect_reason": "10 sends in a row failed — possible ban or no balance"
  },
  "time": "2026-06-11T10:00:00Z"
}
```

### Webhook Retry

Webhook delivery is reliable within the configured in-memory retry window:

- HTTP client timeout: 15 seconds.
- Success: any HTTP `2xx`.
- Failure: network error, timeout or non-2xx response.
- Retry delay: `base_sec`, then doubled on each failed attempt.
- Maximum age: `webhook_retry.max_hours`.
- Queue capacity: 10,000 pending events.
- Delivery concurrency: 16 in-flight webhook requests.

When a webhook event expires, the queue is full, or the process shuts down with
pending webhook events, the event is written to `goip-bridge.fallback.jsonl`
next to the config file. The bridge does not replay this file automatically.

## HTTP Error Summary

| Status | Example body | Meaning |
|---:|---|---|
| `400` | `{"error":"bad json"}` | Invalid JSON body or body too large. |
| `400` | `{"error":"need to + text"}` | `/sms` missing recipient or text. |
| `400` | `{"error":"bad number"}` | Recipient number failed validation. |
| `400` | `{"error":"need code"}` | `/ussd` missing USSD code. |
| `401` | `{"error":"unauthorized"}` | Missing or wrong bearer token. |
| `404` | `{"error":"no alive line"}` | Requested line is not alive, or no alive line exists. |
| `404` | `{"error":"no queue (db off)"}` | `/status` or `/message` used while MySQL queue is unavailable. |
| `405` | `{"error":"method not allowed"}` | Wrong HTTP method. |
| `409` | `{"error":"too late","status":"sending"}` | Cancellation was requested after the row left `queued`. |
| `500` | `{"error":"enqueue failed"}` | Insert into MySQL queue failed. |
| `503` | `{"error":"queue temporarily unavailable (db reconnecting)"}` | MySQL is configured but disconnected. |

## Security Notes

- Keep `listen_http` on `127.0.0.1:8080` unless another host really needs API
  access.
- Use a strong `http_token` if the API is reachable over the network.
- Restrict network access with firewall, VPN or reverse proxy.
- Logs, `/inbox`, MySQL rows and fallback files may contain phone numbers and
  SMS text.

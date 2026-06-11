# goip-bridge v0.3.2

GoIP SMS/USSD gateway for GoIP DBL / Hybertone: HTTP API, webhook and optional MySQL/MariaDB inbox/outbox queue.

## Download

For normal users, download the ready files from GitHub Releases -> Assets:

- `goip-bridge`
- `goip-bridge-linux-amd64.tar.gz`
- `checksums.txt`

Do not download `Source code` if you only want to run the program.

## Quick Start

```sh
tar -xzf goip-bridge-linux-amd64.tar.gz
cd goip-bridge-linux-amd64
./goip-bridge -config config.json -init en
./goip-bridge -config config.json
```

## Changes

See `CHANGELOG.md` section `[0.3.2]`.

## Documentation

- README.md
- INSTALL.md
- CONFIG.md
- API.md
- MYSQL.md
- FIREWALL.md
- TROUBLESHOOTING.md
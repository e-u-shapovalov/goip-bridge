CREATE DATABASE IF NOT EXISTS goip_go
  CHARACTER SET utf8mb4
  COLLATE utf8mb4_unicode_ci;

CREATE USER IF NOT EXISTS 'goip_bridge'@'127.0.0.1'
  IDENTIFIED BY 'CHANGE_ME_STRONG_DB_PASSWORD';

ALTER USER 'goip_bridge'@'127.0.0.1'
  IDENTIFIED BY 'CHANGE_ME_STRONG_DB_PASSWORD';

GRANT SELECT, INSERT, UPDATE ON goip_go.* TO 'goip_bridge'@'127.0.0.1';
FLUSH PRIVILEGES;

USE goip_go;

CREATE TABLE IF NOT EXISTS goip_inbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  line VARCHAR(64) NOT NULL,
  from_number VARCHAR(64) NOT NULL,
  text TEXT NOT NULL,
  received_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  PRIMARY KEY (id),
  KEY idx_received_at (received_at),
  KEY idx_line_received_at (line, received_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

CREATE TABLE IF NOT EXISTS goip_outbox (
  id BIGINT UNSIGNED NOT NULL AUTO_INCREMENT,
  guid VARCHAR(64) NULL,
  line VARCHAR(64) NULL,
  type VARCHAR(8) NOT NULL DEFAULT 'sms',
  to_number VARCHAR(64) NOT NULL,
  text TEXT NULL,
  status ENUM('queued','sending','sent','delivered','done','failed','cancelled') NOT NULL DEFAULT 'queued',
  sms_no BIGINT NULL,
  error_code VARCHAR(255) NULL,
  reply TEXT NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  sent_at DATETIME NULL,
  delivered_at DATETIME NULL,
  PRIMARY KEY (id),
  KEY idx_status_id (status, id),
  KEY idx_line_status (line, status),
  KEY idx_sms_no (sms_no),
  UNIQUE KEY idx_guid (guid)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

-- type='sms'  : to_number = recipient, text = body, result -> sms_no / status sent->delivered
-- type='ussd' : to_number = USSD code (e.g. *100#), text = NULL, result -> reply / status done
-- type='cmd'  : to_number = control command ('status' | 'reset'), text = NULL. The row is just the
--               trigger: the bridge marks it status='done' and writes the REPLY as one JSON row into
--               the inbox table (line='system', from_number='goip-bridge', body in text) + the webhook.
--               'status' = diagnostics (uptime, RAM, lines, queue counts); 'reset' = cancel all 'queued'
--               rows (no DELETE grant, so they become 'cancelled') + flush in-RAM caches, no restart.
--               (Same as POST /stats and POST /reset on the HTTP API.)
-- guid is the public id returned by POST /sms|/ussd and used by GET /status/{guid}, DELETE /message/{guid}.
--   Multiple NULL guids are allowed (legacy rows); the scheduler assigns a guid when it claims such a row.
--
-- Upgrading an existing table (already-deployed installs):
--   ALTER TABLE goip_outbox
--     ADD COLUMN guid  VARCHAR(64) NULL AFTER id,
--     ADD COLUMN type  VARCHAR(8)  NOT NULL DEFAULT 'sms' AFTER line,
--     ADD COLUMN reply TEXT NULL AFTER error_code,
--     MODIFY status ENUM('queued','sending','sent','delivered','done','failed','cancelled') NOT NULL DEFAULT 'queued',
--     ADD UNIQUE KEY idx_guid (guid);
-- MODIFY status is REQUIRED before running the async build: the scheduler writes status='sending'
-- and USSD writes 'done'/'cancelled', which an older ENUM lacking those values would reject.
-- If guid was already added earlier as a plain KEY, convert it instead:
--   ALTER TABLE goip_outbox DROP INDEX idx_guid, ADD UNIQUE KEY idx_guid (guid);
-- (UNIQUE assumes no duplicate non-NULL guids exist yet — true for legacy rows, which all have guid NULL.)
--
-- To also tighten loose legacy column types/collation (signed id, VARCHAR(32), nullable cols,
-- INT sms_no, MariaDB utf8mb4_uca1400_ai_ci) to exactly match the definitions above, see the
-- runnable ALTER blocks in MYSQL.md -> "Выравнивание legacy-типов к строгой схеме".

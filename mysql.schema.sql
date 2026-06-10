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
  line VARCHAR(64) NULL,
  to_number VARCHAR(64) NOT NULL,
  text TEXT NOT NULL,
  status ENUM('queued','sending','sent','delivered','failed') NOT NULL DEFAULT 'queued',
  sms_no BIGINT NULL,
  error_code VARCHAR(255) NULL,
  created_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP,
  sent_at DATETIME NULL,
  delivered_at DATETIME NULL,
  PRIMARY KEY (id),
  KEY idx_status_id (status, id),
  KEY idx_line_status (line, status),
  KEY idx_sms_no (sms_no)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_unicode_ci;

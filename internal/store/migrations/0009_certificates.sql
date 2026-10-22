-- DNS-01 certificates (ADR-024, L3-005 §6): chain and encrypted key per hostname. Caddy keeps the HTTP-01 and
-- internal ones itself.
CREATE TABLE certificates (
  hostname       TEXT PRIMARY KEY,
  domain_id      TEXT NOT NULL REFERENCES resources(id) ON DELETE CASCADE,
  chain_pem      TEXT NOT NULL,
  key_ciphertext BLOB NOT NULL,
  issuer         TEXT NOT NULL DEFAULT '',
  not_before     TEXT NOT NULL,
  not_after      TEXT NOT NULL,
  updated_at     TEXT NOT NULL
);
CREATE INDEX certificates_domain ON certificates (domain_id);

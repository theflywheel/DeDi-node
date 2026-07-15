CREATE TABLE log_entries (
  seq          BIGINT PRIMARY KEY,
  entry_type   TEXT NOT NULL CHECK (entry_type IN ('namespace','registry','record')),
  namespace    TEXT NOT NULL,
  registry     TEXT NOT NULL DEFAULT '',
  record_name  TEXT NOT NULL DEFAULT '',
  version_num  INT  NOT NULL,
  payload_raw  BYTEA NOT NULL,
  payload      JSONB NOT NULL,
  digest       BYTEA NOT NULL CHECK (octet_length(digest) = 32),
  state        TEXT NOT NULL,
  created_by   TEXT NOT NULL,
  created_at   TIMESTAMPTZ NOT NULL,
  leaf_hash    BYTEA NOT NULL CHECK (octet_length(leaf_hash) = 32),
  UNIQUE (entry_type, namespace, registry, record_name, version_num)
);

CREATE INDEX idx_le_resource ON log_entries (namespace, registry, record_name, seq DESC);
CREATE INDEX idx_le_payload  ON log_entries USING GIN (payload jsonb_path_ops);

CREATE TABLE tree_hashes (
  idx  BIGINT PRIMARY KEY,
  hash BYTEA NOT NULL CHECK (octet_length(hash) = 32)
);

CREATE TABLE checkpoints (
  tree_size  BIGINT PRIMARY KEY,
  root_hash  BYTEA NOT NULL CHECK (octet_length(root_hash) = 32),
  note_text  TEXT NOT NULL,
  created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

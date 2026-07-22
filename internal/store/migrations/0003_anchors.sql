-- Ledger anchor receipts: which checkpoints were anchored to an external
-- ledger, where. Kept OUT of the transparency log on purpose: recording an
-- anchor in the log would grow the tree, which would trigger another anchor.
CREATE TABLE anchors (
  id          BIGSERIAL PRIMARY KEY,
  backend     TEXT        NOT NULL,
  tree_size   BIGINT      NOT NULL,
  tx_ref      TEXT        NOT NULL,
  block_ref   TEXT        NOT NULL DEFAULT '',
  anchored_at TIMESTAMPTZ NOT NULL DEFAULT now(),
  UNIQUE (backend, tree_size)
);

-- Served-request counters, one row per status class. Kept OUT of the
-- transparency log: these are operational telemetry, not directory facts, and
-- appending them would grow the tree on every read.
--
-- Written by a periodic flush (not per request), so the row count is fixed and
-- the write rate is independent of traffic. UPSERT is additive, which lets
-- several node replicas share one row per class.
CREATE TABLE request_counts (
  class      TEXT PRIMARY KEY,
  n          BIGINT      NOT NULL DEFAULT 0,
  updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

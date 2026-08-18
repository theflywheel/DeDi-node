-- The status page reads the signing history by time, not by tree size.
--
-- The checkpoints table has carried created_at since 0001 but has only ever
-- been indexed by its tree_size primary key, because every reader until now
-- wanted "the latest" or "all of them in tree order". A time-bounded read —
-- "what did this node sign in the last day" — is a sequential scan without
-- this, and it grows with every checkpoint the node has ever written.
CREATE INDEX IF NOT EXISTS idx_checkpoints_created_at ON checkpoints (created_at DESC);

-- The activity series is the other half of the same page. A checkpoint gap on
-- its own is ambiguous — the node may have been down, or the log may simply
-- have been idle, and internal/checkpoint deliberately signs nothing when the
-- tree has not grown. Entry timestamps are what tell those apart, so they are
-- read by time too.
CREATE INDEX IF NOT EXISTS idx_le_created_at ON log_entries (created_at DESC);

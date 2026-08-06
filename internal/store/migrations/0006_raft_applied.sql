-- How far this replica's materialised state has consumed the replicated
-- command stream.
--
-- Raft keeps its applied index in memory: on restart it replays every
-- committed entry into the state machine, because it assumes the state machine
-- was rebuilt from a snapshot and is therefore empty. That assumption does not
-- hold here — this replica's state is in Postgres and survives the restart, so
-- a replay would apply every command a second time, growing entries the other
-- replicas do not have and forking the tree.
--
-- The index is therefore persisted, and written in the same transaction as the
-- state change it accompanies. Anything less than one transaction leaves a
-- crash window in which the state moved but the index did not.
CREATE TABLE IF NOT EXISTS raft_applied (
    id            BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    applied_index BIGINT      NOT NULL,
    updated_at    TIMESTAMPTZ NOT NULL DEFAULT now()
);

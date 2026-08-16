-- The node's own signing identity, so a node handed nothing but a database can
-- still sign its own checkpoints. The key belongs with the log it signs: a node
-- that came back with a different identity would look, to every witness and
-- client watching it, exactly like a different node serving a forked history.
--
-- Exactly one row, enforced by a primary key that can only hold one value. The
-- alternative -- an unconstrained table -- invites a second identity to appear
-- and leaves "which one signs?" to row order.
CREATE TABLE IF NOT EXISTS node_identity (
    id         BOOLEAN PRIMARY KEY DEFAULT TRUE CHECK (id),
    skey       TEXT        NOT NULL,
    vkey       TEXT        NOT NULL,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- One logical database per replica.
--
-- Replicas must not share a database: each applies every command to its own
-- copy of the state, and two replicas writing the same rows would corrupt it
-- on the first append. Separate databases on one instance is the supported
-- layout (docs/replication.md) — with the caveat recorded there, that it
-- survives a replica dying but not the instance dying.
CREATE DATABASE dedi_r1;
CREATE DATABASE dedi_r2;
CREATE DATABASE dedi_r3;

-- Webhook subscriptions: who wants to be told when a registry gains a version.
--
-- There is no outbox table. The event a consumer needs is "log entry seq N
-- exists", and log_entries already records exactly that, transactionally. A
-- per-subscription cursor over seq gives the same guarantee an outbox is built
-- for — no delivery can exist for a version that was not committed — without a
-- second table that has to be kept in step with the first.
--
-- There is no per-subscription secret either. Deliveries are signed with the
-- node's identity key, so a consumer verifies a push exactly as it verifies a
-- lookup, and anyone can check it. A shared secret would prove less, to fewer
-- people, and would have to be replicated through the Raft log to survive
-- failover.
CREATE TABLE webhook_subscriptions (
  id            TEXT PRIMARY KEY,
  namespace     TEXT NOT NULL,
  registry      TEXT NOT NULL,
  target_url    TEXT NOT NULL,
  state         TEXT NOT NULL,  -- active | deleted
  -- Highest seq confirmed delivered. -1 means nothing yet: a new subscription
  -- starts at the seq it was created at rather than replaying the whole log at
  -- a consumer that never asked for it.
  cursor_seq    BIGINT NOT NULL,
  created_at    TIMESTAMPTZ NOT NULL,
  updated_at    TIMESTAMPTZ NOT NULL
);

-- Delivery only reads active subscriptions, and only ever for one registry.
CREATE INDEX idx_webhook_subs_target ON webhook_subscriptions (namespace, registry)
  WHERE state = 'active';

-- A seq that could not be delivered after the configured attempts. Recording it
-- lets the cursor move past: without this, one consumer rejecting one entry
-- would wedge that subscription forever, and the next revocation — the thing
-- the whole subsystem exists to deliver — would sit behind it.
CREATE TABLE webhook_dead_letters (
  subscription_id TEXT NOT NULL REFERENCES webhook_subscriptions(id) ON DELETE CASCADE,
  seq             BIGINT NOT NULL,
  attempts        INT NOT NULL,
  last_error      TEXT NOT NULL,
  dead_at         TIMESTAMPTZ NOT NULL,
  PRIMARY KEY (subscription_id, seq)
);

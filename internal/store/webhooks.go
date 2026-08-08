package store

import (
	"context"

	"fmt"
	"strings"
	"time"

	"github.com/jackc/pgx/v5"
)

// Webhook subscriptions are replicated state, not local configuration.
//
// This is the part that is easy to get wrong. Every other operator setting on
// this node — peers, keys, intervals — is read from the environment and is
// identical on every replica because the operator deployed it that way. A
// subscription is created at runtime, by an API call, against whichever replica
// happens to be leader. The three replicas hold three separate logical
// databases. So a subscription written straight to the leader's database exists
// on exactly one node, and the day that node loses an election the promoted
// replica has never heard of it: revocations stop being pushed, nothing errors,
// and the subscriber's next clue is a stale key it kept trusting.
//
// Everything durable about a subscription therefore goes through the same Raft
// command stream as the log itself, including the delivery cursor — so a
// failover resumes exactly where the old leader stopped rather than replaying
// history at the consumer or skipping the gap. Only the transient retry state
// (attempt counter, next attempt time) stays in the leader's memory, on the
// same reasoning network.Monitor uses for its observations: it is a fact about
// the world right now, not a fact about the directory.

// WebhookOp is a state transition on the subscription table.
type WebhookOp string

const (
	// WebhookCreate registers a subscription. Its cursor starts at the seq
	// current when it was created, so a new subscriber is not sent the entire
	// history of a log it just asked about.
	WebhookCreate WebhookOp = "create"
	// WebhookDelete retires one. The row is kept in state 'deleted' rather than
	// removed, because its dead letters are the record of what a consumer was
	// never told, and that record should outlive the subscription.
	WebhookDelete WebhookOp = "delete"
	// WebhookAdvance records a confirmed delivery.
	WebhookAdvance WebhookOp = "advance"
	// WebhookDeadLetter gives up on one seq and moves the cursor past it.
	WebhookDeadLetter WebhookOp = "dead_letter"
)

// WebhookCommand is one replicated change to the subscription table. Like
// AppendInput it carries its own clock: a replica may apply this much later,
// and re-reading the local clock would give the replicas different rows.
type WebhookCommand struct {
	Op        WebhookOp `json:"op"`
	ID        string    `json:"id"`
	Namespace string    `json:"namespace,omitempty"`
	Registry  string    `json:"registry,omitempty"`
	TargetURL string    `json:"target_url,omitempty"`
	Seq       int64     `json:"seq,omitempty"`
	Attempts  int       `json:"attempts,omitempty"`
	LastError string    `json:"last_error,omitempty"`
	At        time.Time `json:"at"`
}

// WebhookSubscription is a subscription as stored.
type WebhookSubscription struct {
	ID        string    `json:"id"`
	Namespace string    `json:"namespace"`
	Registry  string    `json:"registry"`
	TargetURL string    `json:"target_url"`
	State     string    `json:"state"`
	CursorSeq int64     `json:"cursor_seq"`
	CreatedAt time.Time `json:"created_at"`
	UpdatedAt time.Time `json:"updated_at"`
}

// DeadLetter is one seq a subscription was never successfully told about.
type DeadLetter struct {
	SubscriptionID string    `json:"subscription_id"`
	Seq            int64     `json:"seq"`
	Attempts       int       `json:"attempts"`
	LastError      string    `json:"last_error"`
	DeadAt         time.Time `json:"dead_at"`
}

func validateWebhook(c *WebhookCommand) error {
	if strings.TrimSpace(c.ID) == "" {
		return fmt.Errorf("%w: webhook command needs an id", ErrInvalidWrite)
	}
	if c.At.IsZero() {
		return fmt.Errorf("%w: webhook command needs a timestamp decided by the proposer, "+
			"not read from each replica's clock", ErrInvalidWrite)
	}
	switch c.Op {
	case WebhookCreate:
		if c.Namespace == "" || c.Registry == "" || c.TargetURL == "" {
			return fmt.Errorf("%w: a subscription needs a namespace, a registry and a target URL", ErrInvalidWrite)
		}
	case WebhookDelete:
	case WebhookAdvance, WebhookDeadLetter:
		if c.Seq < 0 {
			return fmt.Errorf("%w: %s needs a seq", ErrInvalidWrite, c.Op)
		}
	default:
		return fmt.Errorf("%w: unknown webhook op %q", ErrInvalidWrite, c.Op)
	}
	return nil
}

// ApplyWebhook runs the transition on an unreplicated node.
func (s *Store) ApplyWebhook(ctx context.Context, c WebhookCommand) error {
	if c.At.IsZero() {
		c.At = time.Now()
	}
	if err := validateWebhook(&c); err != nil {
		return err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, logWriteLock); err != nil {
		return err
	}
	if err := applyWebhookLocked(ctx, tx, c); err != nil {
		return err
	}
	return tx.Commit(ctx)
}

// ApplyWebhookReplicated runs the transition exactly once, on the same applied
// index discipline as ApplyReplicated — see applied.go for why replay is not
// harmless here either: a replayed create would resurrect a deleted
// subscription and start pushing to a target the operator had retired.
func (s *Store) ApplyWebhookReplicated(ctx context.Context, index uint64, c WebhookCommand) (bool, error) {
	if err := validateWebhook(&c); err != nil {
		return false, err
	}
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return false, err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `SELECT pg_advisory_xact_lock($1)`, logWriteLock); err != nil {
		return false, err
	}
	done, err := alreadyApplied(ctx, tx, index)
	if err != nil {
		return false, err
	}
	if done {
		return true, nil
	}
	if err := applyWebhookLocked(ctx, tx, c); err != nil {
		if isRejection(err) {
			if markErr := s.markApplied(ctx, index); markErr != nil {
				return false, fmt.Errorf("%w (and recording the index failed: %v)", err, markErr)
			}
		}
		return false, err
	}
	if err := recordApplied(ctx, tx, index); err != nil {
		return false, err
	}
	return false, tx.Commit(ctx)
}

func applyWebhookLocked(ctx context.Context, tx pgx.Tx, c WebhookCommand) error {
	at := c.At.UTC().Truncate(time.Microsecond)
	switch c.Op {
	case WebhookCreate:
		// The starting cursor is read inside the transaction, under the same lock
		// that serialises appends, so it cannot straddle a concurrent append and
		// leave an entry that is neither "before the subscription" nor delivered.
		var head int64
		if err := tx.QueryRow(ctx, `SELECT COALESCE(MAX(seq), -1) FROM log_entries`).Scan(&head); err != nil {
			return err
		}
		_, err := tx.Exec(ctx,
			`INSERT INTO webhook_subscriptions
			   (id, namespace, registry, target_url, state, cursor_seq, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,'active',$5,$6,$6)
			 ON CONFLICT (id) DO UPDATE SET
			   namespace=EXCLUDED.namespace, registry=EXCLUDED.registry,
			   target_url=EXCLUDED.target_url, state='active', updated_at=EXCLUDED.updated_at`,
			c.ID, c.Namespace, c.Registry, c.TargetURL, head, at)
		return err

	case WebhookDelete:
		tag, err := tx.Exec(ctx,
			`UPDATE webhook_subscriptions SET state='deleted', updated_at=$2 WHERE id=$1`, c.ID, at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("subscription %q: %w", c.ID, ErrNotFound)
		}
		return nil

	case WebhookAdvance:
		// GREATEST, not assignment: delivery is at-least-once across a failover,
		// so a late confirmation from a deposed leader must not walk the cursor
		// backwards and re-send everything after it.
		tag, err := tx.Exec(ctx,
			`UPDATE webhook_subscriptions SET cursor_seq=GREATEST(cursor_seq, $2), updated_at=$3 WHERE id=$1`,
			c.ID, c.Seq, at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("subscription %q: %w", c.ID, ErrNotFound)
		}
		return nil

	case WebhookDeadLetter:
		if _, err := tx.Exec(ctx,
			`INSERT INTO webhook_dead_letters (subscription_id, seq, attempts, last_error, dead_at)
			 VALUES ($1,$2,$3,$4,$5) ON CONFLICT (subscription_id, seq) DO NOTHING`,
			c.ID, c.Seq, c.Attempts, c.LastError, at); err != nil {
			return err
		}
		tag, err := tx.Exec(ctx,
			`UPDATE webhook_subscriptions SET cursor_seq=GREATEST(cursor_seq, $2), updated_at=$3 WHERE id=$1`,
			c.ID, c.Seq, at)
		if err != nil {
			return err
		}
		if tag.RowsAffected() == 0 {
			return fmt.Errorf("subscription %q: %w", c.ID, ErrNotFound)
		}
		return nil
	}
	return fmt.Errorf("%w: unknown webhook op %q", ErrInvalidWrite, c.Op)
}

const webhookCols = `id, namespace, registry, target_url, state, cursor_seq, created_at, updated_at`

func scanSubscriptions(rows pgx.Rows) ([]WebhookSubscription, error) {
	defer rows.Close()
	out := []WebhookSubscription{}
	for rows.Next() {
		var s WebhookSubscription
		if err := rows.Scan(&s.ID, &s.Namespace, &s.Registry, &s.TargetURL,
			&s.State, &s.CursorSeq, &s.CreatedAt, &s.UpdatedAt); err != nil {
			return nil, err
		}
		out = append(out, s)
	}
	return out, rows.Err()
}

// ActiveSubscriptions lists the subscriptions delivery should consider, oldest
// first so the order is stable across replicas and across restarts.
func (s *Store) ActiveSubscriptions(ctx context.Context) ([]WebhookSubscription, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+webhookCols+` FROM webhook_subscriptions WHERE state='active' ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return scanSubscriptions(rows)
}

// AllSubscriptions lists every subscription including retired ones. It backs
// both the operator console and the Raft snapshot.
func (s *Store) AllSubscriptions(ctx context.Context) ([]WebhookSubscription, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT `+webhookCols+` FROM webhook_subscriptions ORDER BY created_at, id`)
	if err != nil {
		return nil, err
	}
	return scanSubscriptions(rows)
}

// Subscription reads one by id.
func (s *Store) Subscription(ctx context.Context, id string) (WebhookSubscription, error) {
	rows, err := s.pool.Query(ctx, `SELECT `+webhookCols+` FROM webhook_subscriptions WHERE id=$1`, id)
	if err != nil {
		return WebhookSubscription{}, err
	}
	subs, err := scanSubscriptions(rows)
	if err != nil {
		return WebhookSubscription{}, err
	}
	if len(subs) == 0 {
		return WebhookSubscription{}, ErrNotFound
	}
	return subs[0], nil
}

// PendingFor returns the entries a subscription has not been told about yet:
// record entries in its registry, after its cursor, in log order. Namespace and
// registry entries are excluded — a consumer subscribes to a registry to hear
// about the records in it, and the registry's own metadata versions are not
// what a validator re-reads a key for.
func (s *Store) PendingFor(ctx context.Context, sub WebhookSubscription, limit int) ([]Entry, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.pool.Query(ctx,
		`SELECT `+entryCols+` FROM log_entries
		  WHERE entry_type='record' AND namespace=$1 AND registry=$2 AND seq > $3
		  ORDER BY seq LIMIT $4`,
		sub.Namespace, sub.Registry, sub.CursorSeq, limit)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	out := []Entry{}
	for rows.Next() {
		e, err := scanEntry(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, e)
	}
	return out, rows.Err()
}

// AllDeadLetters backs the Raft snapshot.
func (s *Store) AllDeadLetters(ctx context.Context) ([]DeadLetter, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT subscription_id, seq, attempts, last_error, dead_at
		   FROM webhook_dead_letters ORDER BY subscription_id, seq`)
	if err != nil {
		return nil, err
	}
	return scanDeadLetters(rows)
}

// RestoreWebhooks replaces the subscription state from a snapshot.
//
// Unlike log entries, these rows are inserted verbatim rather than replayed
// through the state transition. There is nothing to recompute — no hash, no
// tree — so a replay would only re-derive the cursor from a log the restoring
// replica has not rebuilt yet, and would get it wrong.
func (s *Store) RestoreWebhooks(ctx context.Context, subs []WebhookSubscription, deads []DeadLetter) error {
	tx, err := s.pool.Begin(ctx)
	if err != nil {
		return err
	}
	defer tx.Rollback(ctx)
	if _, err := tx.Exec(ctx, `TRUNCATE webhook_dead_letters, webhook_subscriptions`); err != nil {
		return fmt.Errorf("clear webhook state: %w", err)
	}
	for _, sub := range subs {
		if _, err := tx.Exec(ctx,
			`INSERT INTO webhook_subscriptions
			   (id, namespace, registry, target_url, state, cursor_seq, created_at, updated_at)
			 VALUES ($1,$2,$3,$4,$5,$6,$7,$8)`,
			sub.ID, sub.Namespace, sub.Registry, sub.TargetURL, sub.State,
			sub.CursorSeq, sub.CreatedAt, sub.UpdatedAt); err != nil {
			return fmt.Errorf("restore subscription %s: %w", sub.ID, err)
		}
	}
	for _, d := range deads {
		if _, err := tx.Exec(ctx,
			`INSERT INTO webhook_dead_letters (subscription_id, seq, attempts, last_error, dead_at)
			 VALUES ($1,$2,$3,$4,$5)`,
			d.SubscriptionID, d.Seq, d.Attempts, d.LastError, d.DeadAt); err != nil {
			return fmt.Errorf("restore dead letter %s/%d: %w", d.SubscriptionID, d.Seq, err)
		}
	}
	return tx.Commit(ctx)
}

func scanDeadLetters(rows pgx.Rows) ([]DeadLetter, error) {
	defer rows.Close()
	out := []DeadLetter{}
	for rows.Next() {
		var d DeadLetter
		if err := rows.Scan(&d.SubscriptionID, &d.Seq, &d.Attempts, &d.LastError, &d.DeadAt); err != nil {
			return nil, err
		}
		out = append(out, d)
	}
	return out, rows.Err()
}

// DeadLetters lists what a subscription was never successfully told.
func (s *Store) DeadLetters(ctx context.Context, id string) ([]DeadLetter, error) {
	rows, err := s.pool.Query(ctx,
		`SELECT subscription_id, seq, attempts, last_error, dead_at
		   FROM webhook_dead_letters WHERE subscription_id=$1 ORDER BY seq`, id)
	if err != nil {
		return nil, err
	}
	return scanDeadLetters(rows)
}

package delegation

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"time"
)

// Client is the child side of enrolment: it presents the offer token and its
// own public key to the parent, and keeps trying until it succeeds.
//
// Retrying matters more than it looks. A child is usually deployed by the same
// command that creates its DNS record, so on first boot the parent may not be
// resolvable yet, or the child's own public URL may not route. Giving up after
// one attempt leaves a node that is healthy, serving, and quietly not
// delegated — the failure mode most likely to be mistaken for success, because
// every surface the operator checks looks fine.
type Client struct {
	ParentURL string // parent base URL
	Namespace string // the namespace being claimed
	Token     string // one-time offer secret
	Origin    string // this node's log origin
	Key       string // this node's public verifier key
	SelfURL   string // where this node is reachable

	// ParentKey is the parent's verifier key, recorded alongside the namespace
	// so the child's own log names who granted it authority — checkable
	// against the parent without asking the parent.
	ParentKey string

	// OnEnrolled runs once enrolment has succeeded, including when it was
	// already complete from an earlier boot.
	//
	// It exists because a delegated namespace has to be created in the child's
	// *own* log before the child can publish anything under it. Without that
	// the child comes up enrolled, healthy, and rejecting every write with a
	// missing-namespace error — a state that looks like a working child right
	// up until someone tries to use it.
	OnEnrolled func(ctx context.Context) error

	HTTP     *http.Client
	Interval time.Duration // between attempts; zero uses 15s
}

func (c *Client) httpClient() *http.Client {
	if c.HTTP != nil {
		return c.HTTP
	}
	return &http.Client{Timeout: 15 * time.Second}
}

func (c *Client) interval() time.Duration {
	if c.Interval > 0 {
		return c.Interval
	}
	return 15 * time.Second
}

// Enrolled reports whether the parent already lists this node as the active
// holder of the namespace.
//
// Checked before every attempt, and it is what makes a restart safe: the token
// is single use, so a child that restarts after enrolling would otherwise
// retry forever against a spent offer and log an error loop that reads like a
// broken delegation rather than a completed one.
func (c *Client) Enrolled(ctx context.Context) bool {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet,
		c.ParentURL+"/dedi/delegations/"+parentOf(c.Namespace), nil)
	if err != nil {
		return false
	}
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	var out struct {
		Data struct {
			Children []Record `json:"children"`
		} `json:"data"`
	}
	if json.NewDecoder(resp.Body).Decode(&out) != nil {
		return false
	}
	for _, ch := range out.Data.Children {
		if ch.Namespace == c.Namespace && ch.State == StateActive && ch.ChildOrigin == c.Origin {
			return true
		}
	}
	return false
}

// Once makes a single enrolment attempt.
func (c *Client) Once(ctx context.Context) error {
	body, err := json.Marshal(Enrolment{
		Namespace: c.Namespace, Token: c.Token,
		Origin: c.Origin, Key: c.Key, URL: c.SelfURL,
	})
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.ParentURL+"/dedi/enrol", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.httpClient().Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	msg, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("parent answered %d: %s", resp.StatusCode, bytes.TrimSpace(msg))
	}
	return nil
}

// Run enrols, retrying until it succeeds or ctx is cancelled.
//
// A 410 (offer expired) is terminal: no amount of retrying revives a spent
// offer, and continuing to hammer the parent buries the one log line the
// operator needs to see, which is that they must mint a fresh one.
func (c *Client) Run(ctx context.Context) {
	for {
		if c.Enrolled(ctx) {
			c.settle(ctx)
			return
		}
		err := c.Once(ctx)
		if err == nil {
			c.settle(ctx)
			return
		}
		if isExpired(err) {
			log.Printf("delegation: enrolment offer for %s has expired — mint a fresh one from the parent's admin panel; "+
				"this node is serving but holds no delegation", c.Namespace)
			return
		}
		log.Printf("delegation: enrolment attempt failed (%v); retrying in %s", err, c.interval())
		select {
		case <-ctx.Done():
			return
		case <-time.After(c.interval()):
		}
	}
}

// settle runs the post-enrolment work and reports it.
func (c *Client) settle(ctx context.Context) {
	log.Printf("delegation: enrolled with %s for namespace %s", c.ParentURL, c.Namespace)
	if c.OnEnrolled == nil {
		return
	}
	if err := c.OnEnrolled(ctx); err != nil {
		// Not fatal, and deliberately loud: the node still serves, but it
		// cannot accept writes under the namespace it was just granted.
		log.Printf("delegation: enrolled, but could not create namespace %s in this node's own log: %v — "+
			"writes under it will fail until it exists", c.Namespace, err)
	}
}

func isExpired(err error) bool {
	return err != nil && bytes.Contains([]byte(err.Error()), []byte("410"))
}

// parentOf strips the last dotted label. Child namespaces are exactly one
// level below their parent (ValidateChildNamespace), so this is exact rather
// than a guess.
func parentOf(ns string) string {
	for i := len(ns) - 1; i >= 0; i-- {
		if ns[i] == '.' {
			return ns[:i]
		}
	}
	return ns
}

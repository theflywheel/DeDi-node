package api

import (
	"encoding/hex"
	"encoding/json"
	"strconv"
	"time"

	"github.com/theflywheel/DeDi-node/internal/store"
)

func fmtTime(t time.Time) string { return t.UTC().Format(time.RFC3339Nano) }

func versionID(seq int64) string { return strconv.FormatInt(seq, 10) }

// payloadMeta pulls the well-known top-level keys out of a namespace or
// registry payload. Record payloads are served whole as `details`.
type payloadMeta struct {
	Description string         `json:"description"`
	Domain      string         `json:"domain"`
	Meta        map[string]any `json:"meta"`
	Schema      map[string]any `json:"schema"`
}

func parseMeta(raw []byte) payloadMeta {
	var p payloadMeta
	json.Unmarshal(raw, &p) // best-effort; zero values are valid output
	if p.Meta == nil {
		p.Meta = map[string]any{}
	}
	if p.Schema == nil {
		p.Schema = map[string]any{}
	}
	return p
}

// parseNetworkMemberships pulls the payload's top-level network_memberships
// string array (ONIX dediregistry reads it beside details, not inside).
func parseNetworkMemberships(raw []byte) []string {
	var p struct {
		NetworkMemberships []any `json:"network_memberships"`
	}
	json.Unmarshal(raw, &p)
	out := make([]string, 0, len(p.NetworkMemberships))
	for _, v := range p.NetworkMemberships {
		if s, ok := v.(string); ok {
			out = append(out, s)
		}
	}
	return out
}

type namespaceDTO struct {
	NamespaceID  string         `json:"namespace_id"`
	Name         string         `json:"name"`
	Description  string         `json:"description"`
	Digest       string         `json:"digest"`
	Meta         map[string]any `json:"meta"`
	Version      string         `json:"version"`
	VersionCount int            `json:"version_count"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	CreatedBy    string         `json:"created_by"`
	Domain       string         `json:"domain"`
	State        string         `json:"state"`
	TTL          int            `json:"ttl"`
}

func namespaceData(e store.Entry, versions []store.Entry, ttl int) namespaceDTO {
	p := parseMeta(e.PayloadRaw)
	return namespaceDTO{
		NamespaceID: e.Namespace, Name: e.Namespace,
		Description: p.Description, Digest: hex.EncodeToString(e.Digest), Meta: p.Meta,
		Version: versionID(e.Seq), VersionCount: len(versions),
		CreatedAt: fmtTime(versions[0].CreatedAt), UpdatedAt: fmtTime(e.CreatedAt),
		CreatedBy: e.CreatedBy, Domain: p.Domain, State: e.State, TTL: ttl,
	}
}

type registryDTO struct {
	RegistryID   string         `json:"registry_id"`
	RegistryName string         `json:"registry_name"`
	NamespaceID  string         `json:"namespace_id"`
	Description  string         `json:"description"`
	Digest       string         `json:"digest"`
	Schema       map[string]any `json:"schema"`
	Meta         map[string]any `json:"meta"`
	Version      string         `json:"version"`
	VersionCount int            `json:"version_count"`
	CreatedAt    string         `json:"created_at"`
	UpdatedAt    string         `json:"updated_at"`
	CreatedBy    string         `json:"created_by"`
	State        string         `json:"state"`
	TTL          int            `json:"ttl"`
}

func registryData(e store.Entry, versions []store.Entry, ttl int) registryDTO {
	p := parseMeta(e.PayloadRaw)
	return registryDTO{
		RegistryID: e.Namespace + "/" + e.Registry, RegistryName: e.Registry, NamespaceID: e.Namespace,
		Description: p.Description, Digest: hex.EncodeToString(e.Digest), Schema: p.Schema, Meta: p.Meta,
		Version: versionID(e.Seq), VersionCount: len(versions),
		CreatedAt: fmtTime(versions[0].CreatedAt), UpdatedAt: fmtTime(e.CreatedAt),
		CreatedBy: e.CreatedBy, State: e.State, TTL: ttl,
	}
}

type recordDTO struct {
	RecordID           string          `json:"record_id"`
	RecordName         string          `json:"record_name"`
	RegistryID         string          `json:"registry_id"`
	RegistryName       string          `json:"registry_name"`
	NamespaceID        string          `json:"namespace_id"`
	Namespace          string          `json:"namespace"`
	Description        string          `json:"description"`
	Digest             string          `json:"digest"`
	Details            json.RawMessage `json:"details"` // payload bytes verbatim
	Meta               map[string]any  `json:"meta"`
	NetworkMemberships []string        `json:"network_memberships,omitempty"`
	Version            string          `json:"version"`
	VersionCount       int             `json:"version_count"`
	Genesis            string          `json:"genesis"`
	CreatedAt          string          `json:"created_at"`
	UpdatedAt          string          `json:"updated_at"`
	CreatedBy          string          `json:"created_by"`
	State              string          `json:"state"`
	ValidTill          *string         `json:"valid_till"`
	// Expired / NotYetValid report the payload's declared validity window
	// against the node's clock. They are advisory: the record still resolves,
	// because filtering it would be a read-plane behaviour change for
	// participants whose valid_until is stale or wrong. Present only when the
	// payload declares a window at all.
	//
	// Caveat worth knowing: the ONIX dediregistry client ignores unknown
	// response fields, so today nothing on the network acts on these — expiry
	// is surfaced, not enforced.
	Expired     *bool `json:"expired,omitempty"`
	NotYetValid *bool `json:"not_yet_valid,omitempty"`
	TTL         int   `json:"ttl"`
}

// validityWindow reads the optional valid_from / valid_until payload fields and
// reports them against now. Unparseable values are treated as absent rather
// than as an error: the read plane must keep serving a record whose operator
// wrote a malformed timestamp.
func validityWindow(raw []byte, now time.Time) (validTill *string, expired, notYet *bool) {
	var p struct {
		ValidFrom  string `json:"valid_from"`
		ValidUntil string `json:"valid_until"`
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		return nil, nil, nil
	}
	if p.ValidUntil != "" {
		if t, err := time.Parse(time.RFC3339, p.ValidUntil); err == nil {
			until := p.ValidUntil
			validTill = &until
			e := now.After(t)
			expired = &e
		}
	}
	if p.ValidFrom != "" {
		if t, err := time.Parse(time.RFC3339, p.ValidFrom); err == nil {
			n := now.Before(t)
			notYet = &n
		}
	}
	return validTill, expired, notYet
}

// effectiveTTL resolves how long a consumer may cache this record.
//
// This is the only lever the node has over revocation latency on a Beckn
// network. The ONIX dediregistry client caches lookups in redis and overrides
// its own configured cacheTTL with the `ttl` in our response
// (beckn-onix v1.8.0, dediregistry.go:322) — so a participant revoked here
// keeps validating signatures at every adapter until that TTL elapses.
// Restarting an adapter does not help; the cache is shared and external.
//
// A record may therefore declare its own `ttl` to trade lookup traffic for
// revocation speed: set it low on participants where fast revocation matters.
// Absent or invalid values fall back to the node default (DEDI_TTL).
func effectiveTTL(raw []byte, def int) int {
	var p struct {
		TTL *float64 `json:"ttl"`
	}
	if err := json.Unmarshal(raw, &p); err != nil || p.TTL == nil {
		return def
	}
	if *p.TTL <= 0 || *p.TTL != float64(int(*p.TTL)) {
		return def // zero, negative and fractional seconds are meaningless here
	}
	return int(*p.TTL)
}

func recordData(e store.Entry, versions []store.Entry, ttl int) recordDTO {
	p := parseMeta(e.PayloadRaw)
	validTill, expired, notYet := validityWindow(e.PayloadRaw, time.Now())
	ttl = effectiveTTL(e.PayloadRaw, ttl)
	return recordDTO{
		RecordID:   e.Namespace + "/" + e.Registry + "/" + e.RecordName,
		RecordName: e.RecordName,
		RegistryID: e.Namespace + "/" + e.Registry, RegistryName: e.Registry,
		NamespaceID: e.Namespace, Namespace: e.Namespace,
		Description: p.Description, Digest: hex.EncodeToString(e.Digest),
		Details: json.RawMessage(e.PayloadRaw), Meta: p.Meta,
		NetworkMemberships: parseNetworkMemberships(e.PayloadRaw),
		Version:            versionID(e.Seq), VersionCount: len(versions),
		Genesis:   versionID(versions[0].Seq),
		CreatedAt: fmtTime(versions[0].CreatedAt), UpdatedAt: fmtTime(e.CreatedAt),
		CreatedBy: e.CreatedBy, State: e.State,
		ValidTill: validTill, Expired: expired, NotYetValid: notYet, TTL: ttl,
	}
}

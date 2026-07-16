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
	TTL                int             `json:"ttl"`
}

func recordData(e store.Entry, versions []store.Entry, ttl int) recordDTO {
	p := parseMeta(e.PayloadRaw)
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
		CreatedBy: e.CreatedBy, State: e.State, ValidTill: nil, TTL: ttl,
	}
}

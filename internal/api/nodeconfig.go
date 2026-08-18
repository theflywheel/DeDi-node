package api

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/theflywheel/DeDi-node/internal/provision"
)

// originFor decides a node's log origin.
//
// A replica's belongs to the SET, and there is no sensible default: every
// member must sign under the same one. Defaulting it fed Spec.Validate a
// non-empty value and defeated the very check meant to catch a missing set
// origin, rendering exactly the split-origin replica this endpoint refuses —
// and because only the leader signs, that config looks fine until leadership
// moves to the odd member.
func originFor(role provision.Role, supplied, name string) string {
	if role == provision.RoleReplica {
		return supplied
	}
	return orDefault(supplied, name+"/log")
}

// nodeConfig renders the configuration for a node this operator is standing up
// themselves — a standalone registry, a mirror, a witness, or a member of a
// replica set.
//
// Separate from createChild because they are different operations that only
// look alike. Creating a child MINTS A DELEGATION: it writes a record into this
// node's public log saying a namespace has been granted to someone else, and
// hands out a one-time token to claim it. None of that applies to the other
// four roles — nothing is being granted, there is nothing to claim, and a
// delegation record for a node that will never enrol is a false statement in an
// append-only log, which is the one kind of mistake this system cannot take
// back.
//
// It renders and returns text. Like every provider it calls no cloud API and
// holds no credential; see the package comment on internal/provision.
func (s *Server) nodeConfig(w http.ResponseWriter, r *http.Request) {
	var req struct {
		Role      string `json:"role"`
		Provider  string `json:"provider"`
		NodeName  string `json:"node_name"`
		Namespace string `json:"namespace"`
		Origin    string `json:"origin"`
		Image     string `json:"image"`
		PublicURL string `json:"public_url"`
		DBURL     string `json:"database_url"`

		WitnessTargetURL    string `json:"witness_target_url"`
		WitnessTargetKey    string `json:"witness_target_key"`
		WitnessTargetOrigin string `json:"witness_target_origin"`

		ClusterID        string `json:"cluster_id"`
		ClusterPeers     string `json:"cluster_peers"`
		ClusterBind      string `json:"cluster_bind"`
		ClusterDataDir   string `json:"cluster_data_dir"`
		ClusterBootstrap bool   `json:"cluster_bootstrap"`
		SharedKeyFile    string `json:"shared_key_file"`

		CrawlDomains string `json:"crawl_domains"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		badRequest(w, "body must be JSON")
		return
	}
	role, err := provision.ParseRole(req.Role)
	if err != nil {
		badRequest(w, err.Error())
		return
	}
	if role.Delegated() {
		badRequest(w, "a child is delegated a namespace by this node; create one with "+
			"POST /admin/namespaces/{namespace}/children so it gets an enrolment offer")
		return
	}
	provider, found := provision.Get(orDefault(req.Provider, "env"))
	if !found {
		badRequest(w, "unknown provider; available: "+strings.Join(provision.Names(), ", "))
		return
	}

	name := orDefault(req.NodeName, string(role))
	// A replica's origin is the SET's, and there is no sensible default for
	// it: every member must sign under the same one. Defaulting it here fed
	// Validate a non-empty value and defeated the very check meant to catch a
	// missing set origin — rendering exactly the split-origin replica this
	// endpoint exists to refuse. So the raw value is passed through for a
	// replica and only defaulted where a node genuinely has an origin of its
	// own.
	origin := originFor(role, req.Origin, name)
	spec := provision.Spec{
		Role:      role,
		NodeName:  name,
		Origin:    origin,
		Namespace: req.Namespace,
		Image:     orDefault(req.Image, defaultImage),
		PublicURL: strings.TrimRight(req.PublicURL, "/"),
		// Deliberately not defaulted: a rendered config with a placeholder
		// database is applied by an operator who then replaces it, which is
		// the existing behaviour of every provider here.
		DatabaseURL: req.DBURL,

		WitnessTargetURL:    strings.TrimRight(req.WitnessTargetURL, "/"),
		WitnessTargetKey:    req.WitnessTargetKey,
		WitnessTargetOrigin: req.WitnessTargetOrigin,

		ClusterID:        req.ClusterID,
		ClusterPeers:     req.ClusterPeers,
		ClusterBind:      req.ClusterBind,
		ClusterDataDir:   req.ClusterDataDir,
		ClusterBootstrap: req.ClusterBootstrap,
		SharedKeyFile:    req.SharedKeyFile,

		CrawlDomains: req.CrawlDomains,
	}
	// Refuse rather than render something that cannot do its job. Every one of
	// these omissions fails later and quietly — a replica with no cluster id
	// comes up healthy as an ordinary node with an identity of its own, which
	// is the worst outcome available.
	if err := spec.Validate(); err != nil {
		badRequest(w, err.Error())
		return
	}

	art, err := provider.Render(spec)
	if err != nil {
		internal(w, err)
		return
	}
	ok(w, "Configuration rendered — nothing was published", map[string]any{
		"role": string(role), "artifact": art,
		"providers": provision.Names(), "roles": provision.RoleCatalogue(),
	})
}

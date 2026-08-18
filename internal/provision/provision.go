// Package provision renders the configuration a child node needs to boot.
//
// # Rendering, not executing
//
// Every provider here turns a Spec into text an operator applies. None of them
// call a cloud API, and the daemon holds no cloud credential.
//
// That is a deliberate limit rather than an unfinished one. A registry daemon
// that holds a token able to create and destroy infrastructure has taken on a
// blast radius wildly out of proportion to its job: the same process that
// answers public unauthenticated lookups could, if compromised, delete the
// cluster. Handing back a manifest keeps the credential with the operator and
// their existing deploy path — which already has review, audit and rollback
// that a POST from a web form does not.
//
// The Provider interface is the seam. A provider that *does* execute — Pulumi
// Automation API, a Terraform runner, an internal control plane — implements
// the same interface and is registered the same way; it simply also needs
// credentials, and that decision stays with whoever wires it in.
package provision

import (
	"encoding/json"
	"fmt"
	"sort"
	"strings"
	"text/template"
)

// Spec is everything a child node needs to come up and enrol. It is provider
// independent on purpose: adding a provider must not require changing what the
// delegation layer produces.
type Spec struct {
	NodeName  string // human label, e.g. "beckn-mobility"
	Origin    string // log origin the child will sign under
	Namespace string // the delegated namespace
	Image     string // container image to run

	ParentURL   string // parent base URL, for enrolment and witnessing
	ParentKey   string // parent verifier key, so the child can verify its parent
	EnrolToken  string // one-time secret; see delegation.TokenTTL
	PublicURL   string // where the child will be reachable, once known
	DatabaseURL string // optional; blank leaves a placeholder for the operator

	// Role is what this node will be to the others. Empty means child, which
	// is what every caller predating roles was provisioning. See role.go.
	Role Role

	// WitnessTarget composes with ANY role: a child is normally witnessed by
	// its parent, and a standalone registry may witness a peer. Blank means
	// this node witnesses nobody.
	WitnessTargetURL    string
	WitnessTargetKey    string
	WitnessTargetOrigin string

	// Replica only. The set this node is a member of.
	//
	// SharedKeyFile is where the set's ONE identity key lives. The daemon
	// refuses to start with DEDI_CLUSTER_ID and no shared identity, so a
	// replica rendered without this never boots.
	ClusterID        string
	ClusterPeers     string
	ClusterBind      string
	ClusterDataDir   string
	ClusterBootstrap bool
	SharedKeyFile    string

	// Mirror only. Domains whose published files this node pulls.
	CrawlDomains string
}

// Artifact is rendered output ready to be applied.
type Artifact struct {
	Provider string `json:"provider"`
	Filename string `json:"filename"`
	Content  string `json:"content"`
	// Notes are shown next to the artifact. They carry the things an operator
	// will otherwise get wrong — chiefly that the token expires and that the
	// child must be reachable before it can enrol.
	Notes []string `json:"notes,omitempty"`
}

// Provider renders a Spec for one deployment target.
type Provider interface {
	Name() string
	Render(Spec) (Artifact, error)
}

var providers = map[string]Provider{}

// Register adds a provider. Called from init in this package for the built-ins;
// exported so an operator building a custom dedid can link one in without
// modifying this file.
func Register(p Provider) { providers[p.Name()] = p }

// Get resolves a provider by name.
func Get(name string) (Provider, bool) {
	p, ok := providers[strings.TrimSpace(strings.ToLower(name))]
	return p, ok
}

// Names lists registered providers, sorted, for the admin UI to offer.
func Names() []string {
	out := make([]string, 0, len(providers))
	for n := range providers {
		out = append(out, n)
	}
	sort.Strings(out)
	return out
}

func init() {
	Register(envProvider{})
	Register(composeProvider{})
	Register(railwayProvider{})
	Register(pulumiProvider{})
}

// commonNotes are true regardless of provider, and each is a mistake seen in
// practice rather than a generic caution.
func commonNotes(s Spec) []string {
	// Every role shares these two. A shared database is immediate corruption
	// rather than slow degradation, and a write plane that opens itself is a
	// door nobody chose to install.
	notes := []string{
		"DEDI_DB_URL must point at a database no other node writes to. Two nodes sharing one is immediate corruption, not a slow degradation.",
	}

	switch role(s) {
	case RoleChild:
		notes = append(notes,
			"The enrolment token is single use and expires within the hour. If the child boots after that, mint a fresh offer — the child will otherwise come up healthy and unenrolled, which looks like success.",
			"The child generates its own identity key on first boot and keeps it on its volume. Do not copy a key in from the parent: a parent that has held the child's private key can forge the child's checkpoints, and the child's log then proves nothing.",
			"No publisher key is included, deliberately: the child's write plane stays shut until its own operator opens it. Generate one there with `dedid pubkeygen -kid <id> -namespace "+s.Namespace+"` and set DEDI_PUBLISHER_KEYS. Until then the child serves reads and accepts no writes.",
		)
	case RoleReplica:
		notes = append(notes,
			"Every member of the set needs the SAME key file contents and the SAME DEDI_ORIGIN. A replica that generates its own key is not a replica, it is a second node claiming the first one's name — and one signing under its own origin breaks the cluster's single-origin invariant the moment leadership moves to it.",
			"Membership is fixed at bootstrap. Set DEDI_CLUSTER_BOOTSTRAP=true on exactly one member, on its first start only, and configure the whole set together: this daemon bootstraps a configuration and has no way to add a member to a running cluster.",
			"Its database must still be its own. Replicas agree through Raft, not through a shared table.",
			"Replication buys uptime, not trust. Three replicas agreeing is one party speaking three times; it proves nothing about the log's history, which is what a witness is for.",
		)
	case RoleWitness:
		notes = append(notes,
			"DEDI_WITNESS_TARGET_KEY is the target's public verifier key, published so this node can check the target's checkpoint signatures itself. Getting it from the target over an unauthenticated channel proves the connection, not the target — obtain it the way you would any other trust anchor.",
			"A witness needs NO publisher key. Its verdicts are appended to its own log directly by the witness loop (internal/witness), not through the HTTP write plane, so there is nothing here for a publisher key to authorise. Setting one would also require DEDI_WILDCARD_NAMESPACES — which a witness has no namespace to fill in — and the node would refuse to start.",
		)
	case RoleMirror:
		notes = append(notes,
			"No publisher key, deliberately. Without one the write plane is not merely refused — it is never routed, so /admin answers 404 rather than 401 and the node does not advertise a door it lacks.",
			"A mirror serves what it has crawled. It cannot be more current than its last crawl, and everything it serves is still checkable against the origin's own checkpoint — which is the point of mirroring a transparency log rather than a database.",
		)
	case RoleStandalone:
		notes = append(notes,
			"Generate a publisher key before the first write: `dedid pubkeygen -kid <id> -namespace "+s.Namespace+"`, then set DEDI_PUBLISHER_KEYS. Until then the node serves reads and /admin is not routed at all.",
			"Nothing witnesses this node yet, so nothing can prove it has not rewritten its own history. Point a witness at it, or arrange for a peer to — a directory nobody checks is a database with extra steps.",
		)
	}

	if s.WitnessTargetURL != "" && role(s) != RoleWitness {
		notes = append(notes,
			"This node also witnesses "+s.WitnessTargetURL+". That is independent of its role: witnessing needs only the target's public checkpoint and key, and no permission from it.",
		)
	}
	return notes
}

// env vars every provider sets identically, so a child deployed by one method
// is configured the same as one deployed by another.
func envPairs(s Spec) [][2]string {
	db := s.DatabaseURL
	if db == "" {
		db = "postgres://USER:PASSWORD@HOST:5432/DATABASE"
	}
	pairs := [][2]string{
		{"DEDI_NODE_NAME", s.NodeName},
		{"DEDI_ORIGIN", s.Origin},
		{"DEDI_LISTEN", ":8080"},
		{"DEDI_DB_URL", db},
	}
	if s.Namespace != "" {
		// A node answers Beckn wildcard lookups only for what it holds.
		// Leaving this unset is refused at boot by the wildcard guard.
		pairs = append(pairs, [2]string{"DEDI_WILDCARD_NAMESPACES", s.Namespace})
	}

	switch role(s) {
	case RoleChild:
		// Enrolment: where to claim the delegation, and with what.
		pairs = append(pairs,
			[2]string{"DEDI_PARENT_URL", s.ParentURL},
			[2]string{"DEDI_PARENT_KEY", s.ParentKey},
			[2]string{"DEDI_ENROL_NAMESPACE", s.Namespace},
			[2]string{"DEDI_ENROL_TOKEN", s.EnrolToken},
		)
	case RoleReplica:
		// A replica shares the identity it replicates; it does not enrol and
		// must not generate a key of its own. cmd/dedid refuses to start with
		// DEDI_CLUSTER_ID and no DEDI_KEY/DEDI_KEY_FILE, so omitting this
		// renders a config that cannot boot at all.
		pairs = append(pairs,
			[2]string{"DEDI_KEY_FILE", orDefault(s.SharedKeyFile, "/keys/cluster.key")},
			[2]string{"DEDI_CLUSTER_ID", s.ClusterID},
			[2]string{"DEDI_CLUSTER_PEERS", s.ClusterPeers},
			[2]string{"DEDI_CLUSTER_BIND", orDefault(s.ClusterBind, "0.0.0.0:7000")},
			[2]string{"DEDI_CLUSTER_DATA_DIR", orDefault(s.ClusterDataDir, "/data/raft")},
		)
		if s.ClusterBootstrap {
			// Exactly one member, on its first start only. Without it on any
			// member, Raft never establishes membership: no leader is elected
			// and every write answers 503.
			pairs = append(pairs, [2]string{"DEDI_CLUSTER_BOOTSTRAP", "true"})
		}
	case RoleMirror:
		if s.CrawlDomains != "" {
			pairs = append(pairs, [2]string{"DEDI_CRAWL_DOMAINS", s.CrawlDomains})
		}
	}

	// Witnessing composes with every role, so it is added after the switch
	// rather than inside it.
	if s.WitnessTargetURL != "" {
		pairs = append(pairs,
			[2]string{"DEDI_WITNESS_TARGET_URL", s.WitnessTargetURL},
			[2]string{"DEDI_WITNESS_TARGET_KEY", s.WitnessTargetKey},
		)
		if s.WitnessTargetOrigin != "" {
			pairs = append(pairs, [2]string{"DEDI_WITNESS_TARGET_ORIGIN", s.WitnessTargetOrigin})
		}
	}

	if s.PublicURL != "" {
		pairs = append(pairs, [2]string{"DEDI_PUBLIC_URL", s.PublicURL})
	}
	return pairs
}

// role resolves a Spec's role, defaulting to child.
func role(s Spec) Role {
	if s.Role == "" {
		return RoleChild
	}
	return s.Role
}

func orDefault(v, def string) string {
	if strings.TrimSpace(v) == "" {
		return def
	}
	return v
}

type envProvider struct{}

func (envProvider) Name() string { return "env" }

func (envProvider) Render(s Spec) (Artifact, error) {
	var b strings.Builder
	b.WriteString("# Child node " + s.NodeName + " — delegated namespace " + s.Namespace + "\n")
	b.WriteString("# Apply to your host, then start the dedid image.\n\n")
	for _, kv := range envPairs(s) {
		fmt.Fprintf(&b, "%s=%s\n", kv[0], shellQuote(kv[1]))
	}
	return Artifact{Provider: "env", Filename: ".env." + s.NodeName, Content: b.String(), Notes: commonNotes(s)}, nil
}

type composeProvider struct{}

func (composeProvider) Name() string { return "compose" }

var composeTmpl = template.Must(template.New("compose").Parse(
	`# Child node {{.Spec.NodeName}} — delegated namespace {{.Spec.Namespace}}
services:
  {{.Spec.NodeName}}:
    image: {{.Spec.Image}}
    restart: unless-stopped
    ports:
      - "8080:8080"
    volumes:
      # The identity key lives here. Lose this volume and the child comes back
      # with a new key, which reads to every relying party as a different node.
      - {{.Spec.NodeName}}-data:/data
    environment:
{{range .Env}}      {{index . 0}}: {{printf "%q" (index . 1)}}
{{end}}volumes:
  {{.Spec.NodeName}}-data:
`))

func (composeProvider) Render(s Spec) (Artifact, error) {
	var b strings.Builder
	if err := composeTmpl.Execute(&b, struct {
		Spec Spec
		Env  [][2]string
	}{s, envPairs(s)}); err != nil {
		return Artifact{}, err
	}
	return Artifact{Provider: "compose", Filename: "docker-compose." + s.NodeName + ".yml",
		Content: b.String(), Notes: commonNotes(s)}, nil
}

type railwayProvider struct{}

func (railwayProvider) Name() string { return "railway" }

func (railwayProvider) Render(s Spec) (Artifact, error) {
	var b strings.Builder
	b.WriteString("#!/usr/bin/env bash\n")
	b.WriteString("# Child node " + s.NodeName + " — delegated namespace " + s.Namespace + "\n")
	b.WriteString("# Run where the Railway CLI is already authenticated.\n")
	b.WriteString("set -euo pipefail\n\n")
	fmt.Fprintf(&b, "railway add --service %s\n", shellQuote(s.NodeName))
	fmt.Fprintf(&b, "railway volume add --mount-path /data --service %s\n", shellQuote(s.NodeName))
	for _, kv := range envPairs(s) {
		fmt.Fprintf(&b, "railway variables --service %s --set %s\n",
			shellQuote(s.NodeName), shellQuote(kv[0]+"="+kv[1]))
	}
	fmt.Fprintf(&b, "railway domain --service %s\n", shellQuote(s.NodeName))
	fmt.Fprintf(&b, "railway up --service %s\n", shellQuote(s.NodeName))
	notes := append(commonNotes(s),
		"Railway assigns the public domain only after the service exists, so DEDI_PUBLIC_URL may need setting once `railway domain` prints it.")
	return Artifact{Provider: "railway", Filename: "deploy-" + s.NodeName + ".sh",
		Content: b.String(), Notes: notes}, nil
}

// pulumiProvider emits a Pulumi program rather than driving Pulumi in-process.
//
// The Automation API would let the node run this itself, and that is a real
// option — but it needs a cloud credential and a state backend inside the
// daemon, which is the blast radius this package's doc comment declines to
// take on by default. Emitting the program keeps `pulumi up` where the rest of
// an operator's infrastructure review already happens.
type pulumiProvider struct{}

func (pulumiProvider) Name() string { return "pulumi" }

func (pulumiProvider) Render(s Spec) (Artifact, error) {
	env, err := json.MarshalIndent(envMap(s), "        ", "  ")
	if err != nil {
		return Artifact{}, err
	}
	var b strings.Builder
	b.WriteString(`// Child node ` + s.NodeName + ` — delegated namespace ` + s.Namespace + `
//
// Provider-agnostic on purpose: the environment below is the contract, and any
// compute that can run a container with a persistent /data volume will do.
// Swap the docker provider for ECS, Cloud Run or Fly without touching it.
import * as pulumi from "@pulumi/pulumi";
import * as docker from "@pulumi/docker";

const env = `)
	b.Write(env)
	b.WriteString(`;

const image = "` + s.Image + `";

const container = new docker.Container("` + s.NodeName + `", {
    image,
    restart: "unless-stopped",
    envs: Object.entries(env).map(([k, v]) => ` + "`${k}=${v}`" + `),
    ports: [{ internal: 8080, external: 8080 }],
    // Losing this volume loses the child's identity key, and it comes back as
    // a node nobody has ever seen before.
    volumes: [{ volumeName: "` + s.NodeName + `-data", containerPath: "/data" }],
});

export const containerName = container.name;
`)
	notes := append(commonNotes(s),
		"This program is emitted, not executed — the node holds no cloud credential. Run it with `pulumi up` from wherever your infrastructure normally deploys.")
	return Artifact{Provider: "pulumi", Filename: "index.ts", Content: b.String(), Notes: notes}, nil
}

func envMap(s Spec) map[string]string {
	m := map[string]string{}
	for _, kv := range envPairs(s) {
		m[kv[0]] = kv[1]
	}
	return m
}

// shellQuote renders a value safe to paste into a shell. Namespaces and
// origins are constrained, but a database URL contains a password chosen by
// someone else and routinely holds characters a shell would act on.
func shellQuote(v string) string {
	if v == "" {
		return "''"
	}
	safe := true
	for _, r := range v {
		if !(r == '-' || r == '_' || r == '.' || r == '/' || r == ':' || r == '+' || r == '=' ||
			(r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9')) {
			safe = false
			break
		}
	}
	if safe {
		return v
	}
	return "'" + strings.ReplaceAll(v, "'", `'"'"'`) + "'"
}

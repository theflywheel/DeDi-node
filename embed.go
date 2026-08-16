// Package dedi exists so the node can carry its own documentation.
//
// go:embed cannot reach outside the directory of the package that declares it,
// and the documentation lives in docs/ at the module root — where a reader
// browsing the repository expects to find it, and where every existing link
// already points. Rather than move the files under internal/api to suit the
// embed rule, or keep a second copy there that would drift from the first, this
// package sits at the root where it can see them.
//
// The alternative was to have /docs link out to GitHub, which is what it used
// to do. That makes a node unable to explain itself: an operator who deployed
// one with the button has a running registry and a docs page that is a list of
// links to somebody else's website, useless behind a firewall, wrong the moment
// the repository moves, and silently stale against the binary they are actually
// running. Documentation shipped inside the binary is the same age as the code.
package dedi

import "embed"

// Docs holds the operator-facing documentation, rendered at /docs.
//
// Only the top level is embedded. docs/spec is the LFDT standard as a git
// submodule — it is not ours to serve, it is large, and it is absent entirely
// in a checkout cloned without --recurse-submodules, which would turn a missing
// submodule into a build failure for anyone who just wanted to compile the
// node.
//
//go:embed docs/*.md
var Docs embed.FS

package api

import (
	"html"
	"strings"
)

// The nav is generated rather than written into each page, for the reason
// docs() already gives about the documentation index: a hand-maintained list of
// links stops matching reality the moment someone adds a page.
//
// It had already stopped. Before this, four pages carried four different navs:
//
//	index.html    Explorer  Docs            Demo
//	docs.html     Explorer  Docs            Demo
//	verify.html   Explorer  Docs  Evidence  Demo
//	admin.html    Explorer  Docs  Console
//
// so /verify — the page where a reader checks the node's claims themselves, and
// arguably the point of the whole thing — was reachable from exactly one page:
// the one you were already on. Nobody decided that. It is what a literal link
// list does over time, and it gets worse with every page added.
//
// This is the one part of these pages that no single page owns. The bodies stay
// hand-written HTML; only the cross-page structure is generated.

// navItem is one entry. External is for links off this node, which never take
// the current-page marker.
type navItem struct {
	Href     string
	Label    string
	External bool
}

// navPages lists every page a reader can reach, in the order a reader meets
// them: what this node is, what is in it, who checks it, how to check it
// yourself, and then the reference.
func (s *Server) navPages() []navItem {
	items := []navItem{
		{Href: "/", Label: "Overview"},
		{Href: "/browse", Label: "Browse"},
		{Href: "/verify", Label: "Verify"},
		{Href: "/docs", Label: "Docs"},
	}
	// The console is listed only where it is served. On a read-only node /admin
	// is not routed at all — it answers 404 rather than 401, so that a node with
	// no write plane does not advertise a door it does not have — and a nav
	// entry pointing at that 404 would undo the same reasoning.
	if s.writeEnabled() {
		items = append(items, navItem{Href: "/admin", Label: "Console"})
	}
	items = append(items, navItem{Href: s.demoURL(), Label: "Demo", External: true})
	return items
}

// nav renders the bar, marking current. Every page is linked from every page,
// including the one being viewed: a nav that drops the current entry changes
// width as you move through the site, and leaves a reader unable to see where
// they are relative to everything else.
func (s *Server) nav(current string) string {
	var b strings.Builder
	b.WriteString("<nav>")
	for _, it := range s.navPages() {
		b.WriteString(`<a href="`)
		b.WriteString(html.EscapeString(it.Href))
		b.WriteString(`"`)
		if !it.External && it.Href == current {
			// aria-current carries the meaning; the class is only what paints it.
			// A reader on a screen reader gets the same fact as one looking at
			// bold text.
			b.WriteString(` aria-current="page" class="sel"`)
		}
		b.WriteString(">")
		b.WriteString(html.EscapeString(it.Label))
		b.WriteString("</a>")
	}
	b.WriteString("</nav>")
	return b.String()
}

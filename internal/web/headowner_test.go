package web

import "testing"

// The signed-in owner's header also holds Settings and wraps (two rows from
// 760px, three below), taller than --head-h. Without JavaScript there is no
// --head-seen, so the owner's page keeps a scroll-padding of its own that
// clears the wrapped header; --head-h, which sizes every header, stays as it
// was. (Measured in the browser checks; this pins the rule.)
func TestOwnerHeaderWithoutScript(t *testing.T) {
	bodyHas(t, "app.css", readAsset(t, "static/css/app.css"), []string{
		"html:has(.head-settings) { scroll-padding-top: calc(var(--head-seen, 5.4rem) + 1rem); }",
		"html:has(.head-settings) { scroll-padding-top: calc(var(--head-seen, 8.7rem) + 1rem); }",
		"--head-h: 3.9rem;", ":root { --head-h: 6.9rem; }"}, nil)
}

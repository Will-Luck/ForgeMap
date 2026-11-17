package web

import "testing"

// A link to an in-page target lands below the sticky header, also where the
// header wraps to more lines than --head-h allows for: head.js, loaded on
// both layouts (app.js on the main one, forms.js on the bare one), puts the
// header's measured height in --head-seen, which scroll-padding prefers, and
// scrolls to the target again once it has, on a fresh visit only: on a
// reload or Back the browser puts the reader back where they were. Without
// JavaScript --head-h stands. (No JS harness: the browser checks measure it.)
func TestTargetsLandBelowTheHeader(t *testing.T) {
	bodyHas(t, "app.css", readAsset(t, "static/css/app.css"), []string{"scroll-padding-top: calc(var(--head-seen, var(--head-h)) + 1rem);"}, nil)
	bodyHas(t, "app.js", readAsset(t, "static/js/app.js"), []string{`import "./head.js";`}, nil)
	bodyHas(t, "forms.js", readAsset(t, "static/js/forms.js"), []string{`import "./head.js";`}, nil)
	bodyHas(t, "head.js", readAsset(t, "static/js/head.js"), []string{`document.querySelector(".site-head")`,
		`new ResizeObserver(() => root.style.setProperty("--head-seen", head.offsetHeight + "px")).observe(head);`,
		`const fresh = performance.getEntriesByType("navigation")[0]?.type === "navigate";`,
		`if (id && fresh) document.getElementById(id)?.scrollIntoView();`}, []string{"setAttribute(\"style\""})
}

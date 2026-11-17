package web

import (
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"
)

// Step 1 says, above the form, that a new install can be set up only in its
// first 5 minutes and how long is left, in words the server wrote (so it
// reads right with JavaScript off), with the seconds in data-setup-left for
// forms.js (never a clock time) and a polite status line that the script
// fills only now and then. A part second counts as a whole one. No code
// field is left, and no word of a log.
func TestSetupWindowLine(t *testing.T) {
	h := managed(t, managedSite(), &fakeManager{st: Status{SetupLeft: 251*time.Second + 200*time.Millisecond}})
	rec := get(t, h, "/setup")
	body := rec.Body.String()
	went(t, "step 1", rec, 200, "")
	bodyHas(t, "step 1", body, append([]string{
		`<p class="callout" data-setup-left="252"><svg class="i"`, `<use href="#i-timer">`,
		`<span>A new install can be set up only in its first 5 minutes. Time left: <b data-left-words>4 minutes and 12 seconds</b>.</span></p>`,
		`<p class="sr" role="status" aria-live="polite" data-left-said></p>`, `action="/setup/account"`, stepNow}, bareHas...),
		append([]string{`name="code"`, "container&#39;s log", "Setup time has run out"}, bareLacks...))
	noInline(t, "step 1", body)
	if strings.Index(body, "data-setup-left") > strings.Index(body, `<form class="form" method="post" action="/setup/account">`) {
		t.Error("the time left is not above the form")
	}
}

// The words for a count of seconds, as forms.js writes them.
func TestLeftWords(t *testing.T) {
	for secs, want := range map[int]string{300: "5 minutes", 252: "4 minutes and 12 seconds", 120: "2 minutes", 62: "1 minute and 2 seconds",
		61: "1 minute and 1 second", 60: "1 minute", 59: "59 seconds", 1: "1 second"} {
		if got := leftWords(secs); got != want {
			t.Errorf("leftWords(%d) = %q, want %q", secs, got, want)
		}
	}
}

// A refused step 1 (422) still says how long is left, so the owner knows to
// hurry.
func TestRefusedStepOneKeepsTheTimeLeft(t *testing.T) {
	f := &fakeManager{st: Status{SetupLeft: 90 * time.Second}, err: &FormError{Problems: []FieldProblem{{Field: "password", Message: "Use 12 characters or more."}}}}
	rec := send(t, managed(t, managedSite(), f), http.MethodPost, "/setup/account", false, url.Values{"username": {"mark"}, "password": {"short"}, "confirm": {"short"}}, nil)
	went(t, "refused", rec, 422, "")
	bodyHas(t, "refused", rec.Body.String(), []string{`data-setup-left="90"`, `Time left: <b data-left-words>1 minute and 30 seconds</b>.`, "Use 12 characters or more."}, nil)
}

// Once the window has closed, GET /setup is the closed page (403): bare, the
// 1-2-3 header on step 1, the heading, why, how to restart (the Docker
// command, or stop and start the program) and Try again; no form and no
// input. A step 1 POST the Manager turns away for the window gets the same
// page and keeps nothing typed.
func TestSetupClosedPage(t *testing.T) {
	want := append([]string{`<h1><svg class="i"`, `<use href="#i-hourglass">`, `Setup time has run out</h1>`, stepNow,
		"A new install can be set up only in the first 5 minutes after ForgeMap starts, so that nobody else on the network can claim it.",
		"Restart ForgeMap, then open this page again. With Docker: <code class=\"nobr\">docker restart &lt;container&gt;</code>, or stop and start the forgemap program.",
		`<a class="btn accent" href="/setup">Try again</a>`}, bareHas...)
	lack := append([]string{"<form", "<input", "Time left"}, bareLacks...)

	h := managed(t, managedSite(), &fakeManager{})
	rec := get(t, h, "/setup")
	went(t, "GET /setup, closed", rec, 403, "")
	bodyHas(t, "GET /setup, closed", rec.Body.String(), want, lack)
	noInline(t, "closed", rec.Body.String())

	f := &fakeManager{st: Status{SetupLeft: time.Minute}, err: ErrSetupWindowClosed}
	rec = send(t, managed(t, managedSite(), f), http.MethodPost, "/setup/account", false,
		url.Values{"username": {"mark-typed"}, "password": {"pw-sentinel-5"}, "confirm": {"pw-sentinel-5"}}, nil)
	went(t, "POST after the window", rec, 403, "")
	bodyHas(t, "POST after the window", rec.Body.String(), want, append([]string{"mark-typed", "pw-sentinel-5"}, lack...))
	if !f.did("CreateAccount") {
		t.Error("the handler decided without the Manager")
	}
}

// forms.js counts down from data-setup-left with the browser's monotonic
// clock, writes the same words as leftWords, speaks only at whole minutes
// and at 30 and 10 seconds, and loads /setup at zero. (No JS harness: the
// words are checked here and the behaviour in the browser checks.)
func TestSetupCountdownScript(t *testing.T) {
	bodyHas(t, "forms.js", readAsset(t, "static/js/forms.js"), []string{
		`document.querySelectorAll("[data-setup-left]")`, `const start = Number(line.dataset.setupLeft);`,
		`const left = start - Math.floor((performance.now() - t0) / 1000);`,
		`return sec === 0 ? unit(m, "minute") : unit(m, "minute") + " and " + unit(sec, "second");`,
		`if (said && (left % 60 === 0 || left === 30 || left === 10)) said.textContent = say(left) + " left to set up.";`,
		`location.assign("/setup");`}, []string{"new Date(", "Date.now("})
}

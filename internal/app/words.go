package app

import (
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"strings"

	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/derive"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/sources/gitea"
	"github.com/Will-Luck/forgemap/internal/sources/github"
)

// listShownLocked is a forge's status while its last list failed: words that
// say what to fix where there are some (listWords), else the error, and
// "(showing the last good data)" only once a list has succeeded. A
// browser-set-up install always gets words, Settings' own (ConnWords).
func (a *App) listShownLocked(fs *forgeState) string {
	var text string
	if a.cfg.Origin() == config.OriginSettings {
		text = ConnWords(fs.cfg, fs.listErr)
	} else {
		var ok bool
		if text, ok = a.listWords(fs, fs.listErr); !ok {
			text = "repo list: " + fs.listErr.Error()
		}
	}
	if !fs.listedAt.IsZero() {
		text += " (showing the last good data)"
	}
	return text
}

// listWords puts a failed repo list in words that say what to fix, for the
// mistakes a first run meets (knownWords), naming the token's config key or
// variable. ok is false for any other failure. The words never hold the
// token; the log keeps the error itself, with its HTTP status (listForge).
func (a *App) listWords(fs *forgeState, err error) (string, bool) {
	return knownWords(a.forgeNames[fs.cfg.ID], "check "+a.cfg.TokenSetting(fs.cfg.ID), hostOf(fs.cfg.URL), err)
}

// knownWords puts a failed request to a forge in words that say what to fix,
// for the mistakes a first run meets: a refused token, a missing scope
// (Gitea), a wrong address, a host that cannot be reached. name is how the
// page names the forge, tokenHint what to do about a refused token, host the
// forge's host and port. ok is false for any other failure.
func knownWords(name, tokenHint, host string, err error) (string, bool) {
	var ge *gitea.HTTPError
	var he *github.HTTPError
	status := 0
	switch {
	case errors.As(err, &ge):
		status = ge.Status
	case errors.As(err, &he):
		status = he.Status
	}
	var dns *net.DNSError
	var op *net.OpError
	switch {
	case status == http.StatusUnauthorized:
		return name + " refused the token: " + tokenHint, true
	case status == http.StatusForbidden && ge != nil && giteaMessage(ge.Body) != "":
		// Only Gitea's own answer names a scope; a proxy's 403 page does not.
		return "The token lacks a scope: " + giteaMessage(ge.Body), true
	case status == http.StatusNotFound:
		return "Nothing at this address: check the URL", true
	case errors.As(err, &dns), errors.As(err, &op) && op.Op == "dial":
		return "Cannot reach " + host, true
	}
	return "", false
}

// ConnWords says why a request to forge f failed, in Settings' words: what to
// change, never a config key, the token or any part of an answer's body bar
// Gitea's own scope message. It needs no App (the connection test) and is
// never "". A refused address and a certificate failure arrive inside a dial
// or TLS error, so they are checked before knownWords' dial case. No branch
// reads err.Error(): the text could hold an answer. A Gitea or Forgejo
// certificate can be trusted on the forge's page in Settings, so its words
// point there; GitHub's cannot, so its words are today's. The words send the
// owner to look, never to trust: anyone on the network path can cause them.
func ConnWords(f config.Forge, err error) string {
	name, host := derive.ForgeName(f.Type, f.URL), hostOf(f.URL)
	var unknown x509.UnknownAuthorityError
	var verify *tls.CertificateVerificationError
	var invalid x509.CertificateInvalidError
	var wrongName x509.HostnameError
	trust, guide := ": open this forge in Settings to check it", ""
	if f.Type == "github" {
		trust, guide = "", `: see "Self-signed certificates" in the install guide`
	}
	switch {
	case errors.Is(err, forge.ErrNeedsToken):
		return name + " needs a token: paste one to connect"
	case errors.Is(err, forge.ErrAddressRefused):
		return "ForgeMap will not connect to " + host + ": give the forge's own address"
	case errors.Is(err, forge.ErrPinMismatch):
		return host + " now shows a different certificate from the one you trusted: open this forge in Settings to look at the new one"
	case errors.As(err, &unknown):
		return host + " has a certificate ForgeMap does not trust" + trust + guide
	case errors.As(err, &verify), errors.As(err, &invalid), errors.As(err, &wrongName):
		return host + " has a certificate that is out of date or made for another address" + trust
	}
	// The address was typed into a form, so an answer in Gitea's JSON shape
	// proves nothing: its message is shown only when it names a scope and the
	// answer holds no token anywhere: not in the raw body (the message shown
	// is cut, the body is not) and not in the whole decoded message (a JSON
	// escape hides the token from the raw body).
	var ge *gitea.HTTPError
	if errors.As(err, &ge) && ge.Status == http.StatusForbidden {
		whole := wholeGiteaMessage(ge.Body)
		if strings.Contains(strings.ToLower(whole), "scope") && !strings.Contains(ge.Body, f.Token) && !strings.Contains(whole, f.Token) {
			return "The token lacks a scope: " + giteaMessage(ge.Body)
		}
		return name + " refused access: check the token's permissions"
	}
	if text, ok := knownWords(name, "check it was copied whole and has not expired", host, err); ok {
		return text
	}
	// A spent limit carries no status, and listForge does not log it.
	if _, ok := forge.RateLimited(err); ok {
		return name + " has reached its rate limit for this token: it resets on its own"
	}
	switch code := forge.Status(err); {
	case code == http.StatusForbidden:
		return name + " refused access: check the token's permissions"
	case code >= 300 && code <= 399:
		return "This address redirects elsewhere (often from http:// to https://): give the address the forge itself uses"
	case forge.Timeout(err):
		return host + " did not answer in time"
	}
	return "Could not read " + name + ": ForgeMap's log has the details"
}

// giteaMessage is the message of a Gitea error answer ({"message": ...}),
// trimmed and cut to 160 bytes, or "" when the body is not one (such as a
// proxy's HTML page): that 403 keeps its raw text (listShownLocked).
func giteaMessage(body string) string {
	s := wholeGiteaMessage(body)
	if len(s) > 160 {
		s = strings.ToValidUTF8(s[:160], "")
	}
	return s
}

// wholeGiteaMessage is giteaMessage before the cut: the decoded message, so
// what a JSON escape spells out is readable in it.
func wholeGiteaMessage(body string) string {
	var m struct {
		Message string `json:"message"`
	}
	if json.Unmarshal([]byte(body), &m) != nil {
		return ""
	}
	return strings.TrimSpace(m.Message)
}

// hostOf is the host (and port) of a forge's address: no path, and never a
// password (Validate refuses a URL with one).
func hostOf(u string) string {
	if p, err := url.Parse(u); err == nil && p.Host != "" {
		return p.Host
	}
	return u
}

// emptyNoteLocked is what a forge's source row says when its last list kept
// no repo: why, in words that never name or count a private repo (the log
// gets the counts, once each). listed is how many repos the list gave;
// private and forks how many of them were left out as such; others whether
// the token sees repos the user does not own, which a list with no owners
// leaves out.
func (a *App) emptyNoteLocked(fs *forgeState, listed, private, forks int, others bool) string {
	if len(fs.listing) > 0 {
		return ""
	}
	// A browser-set-up install has no config keys: its notes say what to
	// change in Settings.
	settings := a.cfg.Origin() == config.OriginSettings
	var parts []string
	if private > 0 {
		a.noteOnce(fmt.Sprintf("forge %s: %d private repos left out (include_private is off)", fs.cfg.ID, private))
		if settings {
			parts = append(parts, "Private repos are hidden: Show private repos is off in Settings")
		} else {
			parts = append(parts, "Private repos are hidden (include_private is off)")
		}
	}
	if forks > 0 {
		a.noteOnce(fmt.Sprintf("forge %s: %d forks left out (forks is off)", fs.cfg.ID, forks))
		if settings {
			parts = append(parts, "Forks are left out: forks are off for this forge in Settings")
		} else {
			parts = append(parts, "Forks are left out (forks is off)")
		}
	}
	if listed == 0 && len(fs.cfg.Owners) == 0 && others {
		who := fs.me
		if who == "" {
			who = "the token user"
		}
		if settings {
			parts = append(parts, "No repos owned by "+who+": tick the owners to show on this forge in Settings")
		} else {
			parts = append(parts, "No repos owned by "+who+": add your orgs to owners")
		}
	}
	return strings.Join(parts, ". ")
}

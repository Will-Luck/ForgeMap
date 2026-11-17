package manage

import (
	"context"
	"log"
	"slices"
	"strings"

	"github.com/Will-Luck/forgemap/internal/app"
	"github.com/Will-Luck/forgemap/internal/config"
	"github.com/Will-Luck/forgemap/internal/forge"
	"github.com/Will-Luck/forgemap/internal/settings"
	"github.com/Will-Luck/forgemap/internal/web"
)

// The words of a certificate the normal check refused (the page's panel),
// and of the trust actions' refusals.
const (
	wordCertUntrusted = "ForgeMap does not trust this forge's certificate. It is shown below: trust it only if it is your forge's own."
	wordCertChanged   = "This forge now shows a different certificate from the one trusted. The one it shows now is below."
	wordCertUnread    = "ForgeMap does not trust this forge's certificate, and could not read it to show it here. Try again in a moment."
	wordStillNeeded   = "This forge's certificate still fails ForgeMap's normal check, so it stays trusted. Nothing was changed."
	wordTrustValue    = "That is not a certificate fingerprint, so nothing was saved."
	wordTrustGitHub   = "GitHub's certificate is always checked the normal way, so it cannot be trusted here."
	wordTrustHTTPS    = "Only a forge on an https:// address has a certificate to trust."
	wordChanged       = "This forge was changed while it was being tested. Open it again and try once more."
	wordCertPasses    = "This forge's certificate passes ForgeMap's normal check now, so there is no certificate to trust. Try again without trusting one."
)

// reasons put forge.CertProblem in words: why the normal check refuses a
// certificate.
var reasons = map[forge.CertProblem]string{
	forge.CertOK:               "It passes ForgeMap's normal check, so this forge no longer needs a trusted certificate: press Stop trusting.",
	forge.CertUnknownAuthority: "It is not signed by an authority ForgeMap knows. A certificate a forge made for itself looks like this.",
	forge.CertExpired:          "It is out of date, or not valid yet.",
	forge.CertWrongName:        "It was made for another address.",
	forge.CertOther:            "ForgeMap's normal check refuses it.",
}

// pinProblem is why pin cannot be trusted for a forge of type typ at
// address, "" when it can; pin is given back as a pin (lower case).
func pinProblem(typ, address, pin string) (string, string) {
	p, ok := forge.ParsePin(pin)
	switch {
	case !ok:
		return "", wordTrustValue
	case typ == "github":
		return "", wordTrustGitHub
	case !isHTTPS(address):
		return "", wordTrustHTTPS
	}
	return p, ""
}

// isHTTPS reports whether address is an https:// one, in any case: config
// accepts HTTPS:// (url.Parse lowers the scheme) and keeps it as typed.
func isHTTPS(address string) bool {
	return len(address) >= 8 && strings.EqualFold(address[:8], "https://")
}

// certPanel is the panel for a connection test of fg that failed with err:
// the certificate the forge shows now and the words above it. ok is false
// when err is not the certificate's, or fg cannot be trusted (GitHub, or
// not https://), or the certificate passes the normal check while fg has no
// pin; words alone then say why.
func (m *Manager) certPanel(ctx context.Context, fg config.Forge, err error) (cv *web.CertView, top string, ok bool) {
	if !forge.CertRefused(err) || fg.Type == "github" || !isHTTPS(fg.URL) {
		return nil, "", false
	}
	c, rerr := m.readCert(ctx, fg.URL)
	switch {
	case rerr != nil:
		log.Printf("settings: the certificate of %s could not be read: %v", fg.URL, rerr)
		return nil, wordCertUnread, true
	case c.Problem == forge.CertOK && fg.Cert == "":
		return nil, wordCertPasses, true
	}
	top = wordCertUntrusted
	if fg.Cert != "" {
		top = wordCertChanged
	}
	return certView(c), top, true
}

// certView is certificate c as the page's panel shows it.
func certView(c forge.Certificate) *web.CertView {
	return &web.CertView{Host: c.Host, Reason: reasons[c.Problem], IssuedTo: c.Subject, IssuedBy: c.Issuer,
		ValidUntil: c.NotAfter, Pin: c.SHA256, Passes: c.Problem == forge.CertOK}
}

// testFailed is the refusal of a connection test that failed with err: the
// certificate panel when the certificate is why, else ConnWords' words.
// ticked: the form posted fg.Cert to trust. When the forge answered through
// it and refused something else (a mistyped token), the panel stays under
// ConnWords' words, unticked, so the retry can tick it again at once.
func (m *Manager) testFailed(ctx context.Context, fg config.Forge, err error, ticked bool) error {
	cv, top, ok := m.certPanel(ctx, fg, err)
	if !ok {
		fe := &web.FormError{Problems: []web.FieldProblem{{Message: app.ConnWords(fg, err)}}}
		if ticked && forge.Status(err) != 0 {
			if c, rerr := m.readCert(ctx, fg.URL); rerr == nil && c.Problem != forge.CertOK {
				fe.Cert = certView(c)
			}
		}
		return fe
	}
	if cv != nil && cv.Passes {
		// Only the forge's own page has Stop trusting: here, words alone.
		cv, top = nil, wordCertPasses
	}
	return &web.FormError{Problems: []web.FieldProblem{{Message: top}}, Cert: cv}
}

// TrustForge tests forge id with its saved token through a client pinned to
// pin, and saves the pin only when that passes.
func (m *Manager) TrustForge(ctx context.Context, id, pin string) error {
	saved, err := m.savedForge(id)
	if err != nil {
		return err
	}
	p, why := pinProblem(saved.Type, saved.URL, pin)
	if why != "" {
		return formError("", why)
	}
	fg := saved
	fg.Cert = p
	acct, err := m.describe(ctx, fg, m.version, m.client)
	if err != nil {
		// The words say what is trusted now: the saved pin, not the one posted.
		return m.testFailed(ctx, saved, err, false)
	}
	return m.setCert(fg, acct)
}

// StopTrusting tests forge id with its saved token and the normal check, and
// removes its pin only when that passes.
func (m *Manager) StopTrusting(ctx context.Context, id string) error {
	fg, err := m.savedForge(id)
	if err != nil || fg.Cert == "" {
		return err
	}
	fg.Cert = ""
	acct, err := m.describe(ctx, fg, m.version, m.client)
	switch {
	case forge.CertRefused(err):
		return formError("", wordStillNeeded)
	case err != nil:
		return formError("", app.ConnWords(fg, err))
	}
	return m.setCert(fg, acct)
}

// savedForge is forge id as the file holds it, normalised.
func (m *Manager) savedForge(id string) (config.Forge, error) {
	f := m.store.Get()
	i := forgeIndex(f.Config.Forges, id)
	if i < 0 {
		return config.Forge{}, web.ErrNoSuchForge
	}
	return config.FromManaged(f.Config, m.getenv).Forges[i], nil
}

// setCert saves fg.Cert as the forge's trusted certificate, when the file
// still holds the address and token it was tested with, and applies it.
func (m *Manager) setCert(fg config.Forge, acct forge.Account) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	prev := m.store.Get()
	saved, err := m.store.Update(func(sf *settings.File) error {
		j := forgeIndex(sf.Config.Forges, fg.ID)
		if j < 0 {
			return web.ErrNoSuchForge
		}
		now := config.FromManaged(sf.Config, m.getenv).Forges[j]
		if now.URL != fg.URL || now.Token != fg.Token {
			return formError("", wordChanged)
		}
		forges := slices.Clone(sf.Config.Forges)
		forges[j].Cert = fg.Cert
		sf.Config.Forges = forges
		return nil
	})
	if err != nil {
		return err
	}
	m.remember(fg, acct, nil)
	return m.commitLocked(prev, saved)
}

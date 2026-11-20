package config

import (
	"bytes"
	"os"
	"testing"
)

// The docs say how to move to Settings: the section the README and the
// not-found page's hint send people to (its heading makes the
// #moving-to-settings anchor), its line in the contents and the flag in the
// flags table; the README's install part points there; and the two
// browser-setup sentences an import makes untrue are gone.
func TestDocsSayHowToMoveToSettings(t *testing.T) {
	doc, err := os.ReadFile(configDoc)
	if err != nil {
		t.Fatal(err)
	}
	readme, err := os.ReadFile("../../README.md")
	if err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		name string
		in   []byte
		want string
	}{
		{"the section", doc, "\n## Moving to Settings\n"},
		{"the contents line", doc, "\n- [Moving to Settings](#moving-to-settings)\n"},
		{"the flags table", doc, "\n| `-import-config` | "},
		{"the README", readme, "`docker exec forgemap forgemap -import-config`"},
		{"the README's link", readme, "(https://github.com/Will-Luck/forgemap/blob/main/docs/configuration.md#moving-to-settings)"},
		// A version before 0.3.1 opens setup on an imported folder with
		// the tokens in it and no code to ask for.
		{"the older-version warning", doc, "an older version does not ask for the setup code"},
	} {
		if !bytes.Contains(c.in, []byte(c.want)) {
			t.Errorf("%s: %q is missing", c.name, c.want)
		}
	}
	// Settings shows a GitHub forge's own address (internal/manage's
	// TestGitHubOwnAddressIsShown), so the docs never say it does not; and
	// the start after an import reads no forge before Finish (cmd/forgemap's
	// walk), so the docs never say that start reads them.
	for _, gone := range []string{"is generated, never", "Each forge is tested with its token before it is saved.", "Settings does not show or change it.",
		"it is first read at the start after the import"} {
		if bytes.Contains(doc, []byte(gone)) {
			t.Errorf("%s still says %q", configDoc, gone)
		}
	}
}

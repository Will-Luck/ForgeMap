package web

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"time"
)

// SetupWindow is how long a start that finds no account keeps setup open:
// the Manager counts it from the moment it is made, the page and the log
// say it. Only a restart opens a new one.
const SetupWindow = 5 * time.Minute

// Manager is what the setup, sign-in and Settings handlers ask and act through.
type Manager interface {
	Status() Status                      // the install's state; cheap, read on every request
	Authenticate(r *http.Request) Viewer // what r carries; it changes nothing

	CreateAccount(r *http.Request, f AccountForm) ([]*http.Cookie, error) // setup step 1, while the setup window is open: makes the one account, signs it in
	FinishSetup(f MainForm) error                                         // saves f, marks setup finished and starts the first read

	SignIn(r *http.Request, f SignInForm) ([]*http.Cookie, error)           // checks the password; the session and device cookies
	SignOut(r *http.Request) ([]*http.Cookie, error)                        // the cookie that removes the session, always; with a valid session, ends every session (an error: it could not)
	ChangePassword(r *http.Request, f PasswordForm) ([]*http.Cookie, error) // checks the current password, sets the new one; the changer's new cookie
	NewFeedKey() (string, error)                                            // replaces the feed key and returns the new one, whole, once

	Settings() Settings        // the view model of Settings and of setup steps 2 and 3
	SaveMain(f MainForm) error // validates and saves the main form, and applies it live

	ForgeDetail(ctx context.Context, id string, recheck bool) (ForgeView, error) // one forge's edit form; recheck reads the forge afresh ("Check again"); a GitHub forge with no token is never read
	ConnectForge(ctx context.Context, f ForgeForm) (id string, err error)        // tests f, saves only on a pass (GitHub with no token: saved untested, nothing read); the generated id
	ReplaceToken(ctx context.Context, id string, f ForgeForm) error              // tests forge id with f's token (and address, when given; never on GitHub), saves only on a pass
	SaveForge(id string, f ForgeOptions) error                                   // saves a forge's owners and switches
	RemoveForge(id string) error                                                 // removes a forge
	TrustForge(ctx context.Context, id, pin string) error                        // reads forge id with its saved token through a client pinned to pin; saves the pin only on a pass
	StopTrusting(ctx context.Context, id string) error                           // reads forge id with the normal check; removes its pin only on a pass
}

type Status struct { // the install's state, as the guard needs it
	Admin        bool          // the account exists (setup step 1 is done)
	SetupDone    bool          // setup is finished
	SignInToView bool          // viewing the pages needs the session
	SetupLeft    time.Duration // while there is no account: how long setup stays open; 0 once the window has closed, and always once an account exists
	SetupCode    bool          // while there is no account: step 1 asks for the setup code forgemap -import-config printed
	Hosts        []string      // the names the page answers to besides IP addresses, localhost and names without a dot; empty: any (see refusedHost)
}
type Viewer struct { // what a request carries
	Session bool // a valid session cookie: the signed-in owner
	Key     bool // the feed key, as ?key=, Bearer or Basic
}

type FieldProblem struct{ Field, Message string } // one thing wrong with a form; Field is the control's name, "" for the whole form
type FormError struct {                           // a form the Manager refused: nothing was saved
	Problems []FieldProblem
	Cert     *CertView // the certificate a refused connection test met, offered to trust; nil for none
}

// Error joins the problems with "; ", each as "<Field>: <Message>" (the
// message alone for the whole form). It names controls, never a value.
func (e *FormError) Error() string {
	parts := make([]string, 0, len(e.Problems))
	for _, p := range e.Problems {
		if p.Field == "" {
			parts = append(parts, p.Message)
		} else {
			parts = append(parts, p.Field+": "+p.Message)
		}
	}
	return strings.Join(parts, "; ")
}

var (
	ErrNoSuchForge = errors.New("no such forge")   // no forge has this id
	ErrSetupClosed = errors.New("setup is closed") // this setup step is over (the account exists, or setup is finished)

	ErrSetupWindowClosed = errors.New("setup time has run out") // no account exists and the setup window has closed: only a restart opens a new one

	// A save that could not be applied is wrapped in one of these when it
	// changed something; any other failure changed nothing.
	ErrNothingRuns    = errors.New("neither the new settings nor the previous ones could start")                    // the file holds the previous settings, and the page is empty
	ErrStopping       = errors.New("the process is stopping")                                                       // the file holds the new settings; they apply at the next start
	ErrNotWrittenBack = errors.New("the new settings could not start, and the previous ones were not written back") // the file holds the new settings
)

type AccountForm struct{ Username, Password, Confirm, Code string } // setup step 1; Code is the setup code as typed, "" where step 1 asks for none
type SignInForm struct {                                            // the sign-in form
	Username, Password string
	Keep               bool   // keep me signed in
	Once               string // the one-time token of -reset-password's link, from the form's hidden field; "" for none
}
type PasswordForm struct{ Current, Password, Confirm string } // Settings, Account, change password
type ForgeForm struct{ Type, Address, Token, Trust string }   // "Add a forge" and "Replace token"; Type is gitea, forgejo or github; Trust is the fingerprint ticked in the certificate panel, "" for none
type ForgeOptions struct {                                    // the edit form's Save
	Owners         []string // the ticked owners
	Heatmap, Forks bool
}
type FileRefForm struct{ File, Forge, Repo, Path string } // a projects or ideas file: a path on disk, or a file in a forge's repo; all empty: not set

// MainForm is the main Settings form and setup step 3; Settings.Main carries the current values in it.
type MainForm struct {
	Title, Timezone                     string
	Hosts                               []string // the addresses the page answers to, as typed
	SignInToView                        bool
	IncludePrivate                      bool
	You                                 []string
	NotesType, NotesDir                 string
	NotesMarkers                        []string
	DockerFeed, DockerHost, DockerLabel string
	Projects, Ideas                     FileRefForm
	IdeaLabels                          []string
	IdeaMarker                          string
	NeedsLabels                         []string
	NeedsAssigned, NeedsReviewRequests  bool
	PlanDirs                            []string
	Homepages, Refresh                  bool
}

// Settings is everything the Settings page shows; it holds no token, hash, secret or whole feed key.
type Settings struct {
	Main     MainForm
	Username string
	Forges   []ForgeRow
	KeyTail  string // the feed key's last 4 characters
	Paths    PathChecks
}
type ForgeRow struct { // one connected forge
	ID      string // its name in a projects file, and the {id} of its routes
	Type    string // gitea, forgejo or github
	Name    string // as the page names it: Gitea, GitHub, Codeberg
	Address string // the address its token is sent to; "" for GitHub at its standard address
	Trusted string // the SHA-256 of the certificate the owner trusted, lower-case hex; "" while the normal check applies
	NoToken bool   // a GitHub forge saved with no token: it reads only the public repos a projects file names
}
type ForgeView struct { // a forge's edit form
	ForgeRow
	Login          string // "Connected as <Login>"
	Repos          int    // across every owner
	Capped         bool   // the counts read "or more"
	Owners         []OwnerChoice
	Heatmap, Forks bool
	Problem        string    // why the forge could not be read; "" when it could
	Cert           *CertView // the certificate the forge shows now, when its read failed on it; nil otherwise
}

// CertView is a certificate a forge showed, as the certificate panel shows it.
type CertView struct {
	Host       string    // the host and port it came from
	Reason     string    // why ForgeMap's normal check refuses it, in words
	IssuedTo   string    // who it was issued to
	IssuedBy   string    // who issued it
	ValidUntil time.Time // its last valid moment
	Pin        string    // its SHA-256 in lower-case hex: what a trust form posts
	Passes     bool      // the normal check accepts it: a trusted forge that no longer needs its pin
}
type OwnerChoice struct { // one owner's tick box
	Login  string
	Repos  int
	Picked bool
}

type PathState string // what a check of a configured local path found; "" means "not a local path"
const (
	PathUnset   PathState = "unset"   // nothing is configured
	PathFound   PathState = "found"   // the file or folder is there
	PathMissing PathState = "missing" // it is not
)

type PathChecks struct{ NotesDir, DockerFeed, DockerHost, ProjectsFile, IdeasFile PathState } // the Extra sources' paths as they are now

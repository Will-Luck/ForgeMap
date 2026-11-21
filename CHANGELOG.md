# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

### Changed

- The README tour has a picture of the **Open issues** page.
- The CI and release workflows run newer versions of six pinned actions:
  Docker's QEMU, Buildx, metadata and build-push actions, the GitHub
  release action and the Docker Hub README sync.

## [0.3.1] - 2026-11-20

### Added

- **Open issues page.** The overview's **Open issues** tile now opens a
  page listing every open issue and pull request ForgeMap has read, by
  project, newest updated first. A repo lists up to 50 of each (on Gitea
  and Forgejo, the 50 newest issues; on a GitHub mirror, or a GitHub repo
  read without a token, the issues are taken from the newest opened). A
  mirror, or a GitHub repo read without a token, lists no pull requests.
  Where a repo has more, or has not been read yet, the page says so and
  links to the forge. The command palette has **Open issues** too. At 0
  the tile stays a plain number.
- **Shipped in 30 days** on the overview now opens the ship log at just
  the releases it counts, by month (`/ships?days=30`). **Show all 18
  months** goes back to every release, and the full ship log links to
  the shorter view as **Last 30 days**.
- **Move to Settings.** `forgemap -import-config` (in Docker,
  `docker exec forgemap forgemap -import-config`) copies a config-file or
  quick-start install's forges, tokens and page settings into
  `settings.json`, and prints a one-time setup code and what to do next.
  Start ForgeMap without its config, make your account with the code, and
  the install is set up in the browser with its history. The command
  contacts no forge.
- **A config-file or quick-start install says where Settings is.** Its
  404 for `/settings`, `/setup`, `/signin` and `/signout` keeps its
  heading and buttons, says the install has no Settings page and links
  to **How to move it to Settings**. Typing **settings** in the search
  palette finds **Settings (off on this install)**, the same link; it is
  not listed before you type. While the data folder holds no
  `settings.json`, the start log says once that `forgemap -import-config`
  moves the install to Settings. The header and footer do not change.

### Changed

- `site.json` lists more of each project's open issues and pull requests:
  the new `issues_all` and `prs_all` hold up to 50 open issues and 50 open
  pull requests per repo, and up to 50 of a mirror's issues. Every listed
  item names its repo (`repo`) and a draft pull request says so (`draft`).
  A project with more than one repo of its own lists them, each with its
  open counts, in `issue_repos`. Gitea and Forgejo are asked for 50 at a
  time instead of 10, still in one request per list. The `issues` and
  `prs` lists keep their 10 each (`issues` also keeps up to 10 of a
  mirror's). Needs you and ideas from labels read what they read before.
- **GitHub with no token, set up in the browser.** Add a forge now takes
  GitHub with the token left empty, as a config file always could. A forge
  added with a token is still tested before it is saved; a GitHub forge
  with no token is saved without a test. It reads only the public repos
  your projects file names, at most 60 requests an hour. Settings and
  setup's forge list show it as **No token: public repos only**, its page
  says why, and **Add a token** gives it one later.
- A GitHub forge with an address of its own (set by `url` in a config
  file, and kept by `forgemap -import-config`) now shows that address in
  Settings, in setup's forge list and on its page, as a Gitea forge does,
  so you can see where a token you type there is sent. Settings still
  does not change it.
- A start that stops because its data folder is in use, with no config
  and no `settings.json`, now also says how to keep the folder's history:
  put the config back and run `forgemap -import-config`.

## [0.3.0] - 2026-11-17

### Added

- **Set up in the browser.** A fresh install with no config file, no forge
  variable and an empty data volume no longer stops at start: for its first
  5 minutes it serves a setup page (the log says until when, and a restart
  opens it again); a data folder it cannot write stops the start instead,
  and the log says why. Make the one account, connect Gitea, Forgejo or
  GitHub with a token (tested before it is saved) and choose the page's
  settings. **Settings** then changes every
  setting a config file holds, bar `listen` and `data_dir`, without a
  restart. With **Only me, after signing in** (the default; in Settings,
  **Ask for sign-in to see the page**) the pages, the JSON API and the feed
  need the account; a feed key opens the feed, the JSON API
  and `POST /api/refresh` for feed readers and webhooks. Installs with a
  config file or the quick-start variables work as before, with no
  sign-in.
- **Sign-in**, on an install set up in the browser: one account, a
  password of 12 characters or more, and **Keep me signed in** for 30 days
  (otherwise until the browser closes, or 12 hours at most). Wrong guesses
  are slowed down, and other people's wrong guesses never keep out a
  browser that has signed in before. `-reset-password` makes every browser
  new again, so it also prints a one-time link that lets one sign-in skip a
  wait that others' guesses keep up. Signing out ends every session (and
  says so when it could not), and a password change signs out every other
  device and keeps this one's Keep me signed in. An open page
  whose session has ended goes to the sign-in page; on other installs an
  open page behaves as before.
- **Settings** saves a change and applies it at once, without a restart:
  an unchanged save reads nothing again, and settings that cannot start put
  the previous ones back. Turning sign-in off turns private repos off, and
  no visitor sees a private repo in between.
- **A feed key** (`fm_` and 32 characters) opens `/feed.atom`, the JSON API
  and `POST /api/refresh` while sign-in is on, as `?key=`, a Bearer token
  or a Basic password, so feed readers and push webhooks keep working.
  **Make a new key** shows the new key once and stops the old one at once.
- **Connecting a forge** tests its token first and saves it only if the
  forge answers, then lists the owners the token can read to tick. On an
  install set up in the browser every forge request ignores `HTTP_PROXY`
  and `HTTPS_PROXY`, follows no redirect and refuses link-local addresses
  and the two cloud metadata addresses outside them (`fd00:ec2::254` and
  `100.100.100.200`), and a saved token is only ever sent to the address it
  was saved with.
- **A forge with a self-signed certificate** (or one made for another
  name, or out of date) can be trusted from the page on an install set up
  in the browser: **Connect** shows the certificate and its fingerprint,
  and a tick trusts exactly that certificate for that forge, never any
  other. The forge's page shows the trusted fingerprint and **Stop
  trusting**, and when the forge's certificate changes the overview says
  so and the forge's page offers the new one. Replacing the token of a
  forge that has moved to a certificate the normal check accepts also
  stops trusting the old one. GitHub is always checked the normal way, and
  installs with a config file keep `SSL_CERT_FILE`.
- `forgemap -reset-password` sets a new random password for the account
  of an install set up in the browser, signs every browser out and prints
  the password once, with a one-time sign-in link that skips the wait for
  too many tries (it works for 15 minutes), for example
  `docker exec forgemap forgemap -reset-password`. The running server takes
  it at the next sign-in. It keeps the feed key and the settings, and says
  so. Run as another user than the one `settings.json`
  belongs to, on a disk that keeps each file's owner, it refuses and
  changes nothing, so the server can always read its settings.
- CI cross-builds the binary for Linux arm64 and armv7 and for macOS arm64
  on each push to main and each pull request to main, and builds the image
  for amd64 and arm64, so a break on a release platform shows before a
  release does.
- A weekly workflow scans the latest release's binary with govulncheck. Any
  finding fails it: a Go security fix since the release means a rebuild is
  due.
- The install guide in the wiki has a Docker Compose example and a Caddy
  example of a login in front, and the README's privacy note links both.
- An API page in the wiki: each read-only endpoint and its top-level keys,
  the `/healthz` answer for uptime monitors, what a refresh from a push
  webhook costs, and a dashboard example.
- When a forge lists no repo the page can show, its source row says why.
  On an install with a config file or the quick-start variables it says
  "No repos owned by <user>: add your orgs to owners" (no `owners`, and the
  token sees only other people's repos), "Private repos are hidden
  (include_private is off)" or "Forks are left out (forks is off)"; on an
  install set up in the browser it names the setting to change in Settings
  instead. The page never names or counts the private repos; the log has
  the count. A site with no projects says **Nothing to show yet** on the
  board, with each forge's note or error and where to choose what is
  tracked (Settings on an install set up in the browser, the Installation
  guide otherwise), kept current while the page is open.
- `refresh: false` (or `FORGEMAP_REFRESH=false`) turns refreshes off: the
  palette has no **Refresh now**, and `POST /api/refresh` answers 404.
- `/feed.atom`: an Atom feed of the newest 100 changes the overview shows,
  each titled in the page's words and linked to its project's page, so a
  feed reader or a dashboard can follow the site without it open. Every
  page names it in its head. `?kind=release,ci` keeps some kinds and
  `?project=<slug>` one project; an unknown kind or project gives an empty
  feed. It shows nothing the page does not.
- The Ideas page's filters live in the link: kind, size, project and a
  new **Issue** filter (Filed or Not filed) show in the address, so a
  filtered view can be bookmarked or shared, and the page opens with them
  set. Filed counts every idea that has an issue, ideas from labelled
  issues included, so Filed and Not filed together are all the ideas. The
  kind, size and issue filters work without JavaScript too; the project
  choice needs it. The row of counts above the filters is gone; the filter
  chips carry the counts.

### Changed

- Waymark is now ForgeMap. If you run 0.2.x, change these:
  - Images: `ghcr.io/will-luck/forgemap` and `willluck/forgemap` (were
    `ghcr.io/will-luck/waymark` and `willluck/waymark`, which keep 0.2.x).
  - The binary is `forgemap` and the release files `forgemap-<os>-<arch>`
    (were `waymark` and `waymark-<os>-<arch>`).
  - The config file is `/config/forgemap.yml` (was `/config/waymark.yml`):
    rename it, or point `FORGEMAP_CONFIG` at it. While ForgeMap finds only
    the old file, it does not start, and says why.
  - Environment variables start with `FORGEMAP_` (was `WAYMARK_`):
    `FORGEMAP_CONFIG`, `FORGEMAP_LISTEN`, `FORGEMAP_TITLE` and
    `FORGEMAP_INCLUDE_PRIVATE`, plus the new `FORGEMAP_REFRESH` and
    `FORGEMAP_FORKS`. A `WAYMARK_` variable is no longer read: the log
    warns of each one still set, and `WAYMARK_CONFIG` with no other config
    stops the start.
  - The ideas marker is `forgemap-idea:` by default (was `waymark-idea:`).
    If you filed ideas as issues with 0.2.x, set
    `ideas.marker: "waymark-idea:"` to keep them linked.
  - Requests name the app `ForgeMap/<version>`, and probes
    `ForgeMap-probe/<version>` (were `waymark/<version>` and
    `waymark-probe/<version>`), for a proxy or a firewall that allows or
    counts by User-Agent.
  - The page title is ForgeMap unless `site.title` (or `FORGEMAP_TITLE`)
    sets your own.
  - Your data volume keeps working under its old name: mount it at `/data`
    as before (the examples now call it `forgemap-data`).
  - `scripts/docker-feed.sh` writes to `/var/lib/forgemap-feed` unless
    `FEED_DIR` names another folder (a cron line that sets it, as the docs
    show, keeps working).
  - The repo is github.com/Will-Luck/forgemap; GitHub sends the old address
    there.
  - Forks are left out by default (more below). To keep the forks 0.2.x
    showed, set `forges[].forks: true`, or `FORGEMAP_FORKS=true` in the
    environment-only quick start.
- Two failed probe checks count as "in a row" only when they are at most
  5 minutes apart. A failed check and the next one more than 5 minutes
  later (a long restart, a sleeping host) no longer confirm an outage
  together: the second shows as "Failed once, checking again" until the
  next check. An outage that had already counted stays counted across a
  restart, on a projects-file url and on a homepage alike.
- A release is published only from a commit that passes the tests and the
  demo smoke test, which the release workflow now runs first. Every GitHub
  Action in the workflows is pinned to a full commit SHA, with Dependabot
  proposing the bumps, and govulncheck to a version. The image is pushed
  with a Docker Hub token of its own (the `DOCKERHUB_PUSH_TOKEN` secret,
  Read & Write), and the Docker Hub README is synced by a job of its own:
  the only third-party action given a Docker Hub token (`DOCKERHUB_TOKEN`,
  which the push no longer uses).
- The demo smoke test runs the image read-only, with no Linux capabilities
  and no new privileges, so a write anywhere but `/data` and `/tmp` (where
  the demo keeps its files) fails CI. The install guide's commands use the
  same flags.
- Unchanged repos are read again on minutes of their own in the last
  quarter of the 30 minute (detail) and 6 hour (files) spans, instead of
  all on one tick, so a forge is never asked for every repo at once; no data
  is older than before. With a GitHub token, the commit window's URL holds
  for a day, so an unchanged page is a free 304, and the tag list is asked
  for again only after a push or a release that names a new tag.
- With a GitHub token, a repo whose only news is a run in progress has only
  its runs read each minute, not its whole detail; when the run ends, the
  whole detail is read at once.
- Forks are left out by default: a repo that is a fork becomes a project
  only when the projects file names it, or with `forges[].forks: true`
  (`FORGEMAP_FORKS=true` in the environment-only quick start). Forks made to
  send one change no longer fill the page with Dormant cards. This was a
  known limitation of 0.2.0.
- A forge whose repo list fails says what to fix: "Gitea refused the token:
  check GITEA_TOKEN" (or `forges[N].token` with a config file; GitHub too),
  "The token lacks a scope: ..." with Gitea's own words, "Nothing at this
  address: check the URL" or "Cannot reach <host>". The log keeps the error
  with its HTTP status.
- **Refresh now** is cheap: a refresh lists every forge and reads the
  projects and ideas files again, but reads a repo's detail only where its
  list moved or its last read failed, on Gitea and Forgejo too (0.2.0 read
  every Gitea primary again). Mirrors read in the last 5 minutes, and the
  heatmap, the filed-idea search, F-Droid and the probes read in the last 2
  minutes, wait for their own turn. One refresh a minute at most (was 20
  seconds). The answer says what happened (`"queued": true`, or
  `"queued": false` with `last_at` and `next_at`, beside `"ok": true` as
  before), and the palette says
  "Refresh queued." or "Refreshed at 14:02. The next refresh can start in
  15 s."
- `-fixture` serves an edit of its file within a second and sends it to
  open pages as a live update, so the live page can be tried against a
  site that changes.
- On a phone the map page leads with the **By category** list and keeps
  the map behind **Show map**. Map labels never draw smaller than 13 px on
  screen, at any width.
- The overview says what its two charts count. The week strip has a
  caption with its total, such as "Commits per week, last 12 months:
  967", and each card's last commit line counts its sparkline, such as
  "103 commits in 12 weeks, last 16 hours ago".
- A project on hold is badged **On hold** everywhere (its badge said Parked
  or Frozen, but the map legend said On hold); its page still says whether
  it is parked or frozen, and the badge's icon shows which. A hold with no
  commits since it began no longer adds a Parked or Frozen chip beside the
  badge.
- Every answer's Content Security Policy also says `form-action 'self'`:
  a form on a ForgeMap page can be sent only to ForgeMap itself. This
  applies to every install, including one set up from a config file.
- A start with no config file and no forge variable, on a data volume
  already in use (a config mount gone missing, say), still stops, and now
  says how to set up in the browser instead. A `settings.json` that cannot
  be read stops the start too; it is never moved aside.
- The "listening on" line is logged once the port is bound, on every
  install. Apart from the name (`ForgeMap`, was `waymark`) its words are
  unchanged; a port already in use now shows only the error, with no
  "listening on" line before it.
- `state.json` is written readable by its owner only (mode 0600), like
  `settings.json`, on every install whose disk lets it set the mode; on one
  that does not (a FAT or CIFS mount, say) it is saved as before, and the
  log says so once.

### Fixed

- A link to a place on a page (a forge's **Replace token**, a refused box)
  lands below the sticky header, also where the header takes two rows.
- A date given without a time reads the day it names in every time zone:
  a hold's `since`, a note's `updated:` date, a plan's date (from its file
  name), and "Notes updated N days ago" for a project whose newest note has
  a date only. West of UTC (all of
  the Americas) they showed the day before, or counted a day too many.
- The activity feed names a container in a restart loop, or paused, as
  its card does: "Container X is restarting" or "is paused", not
  "stopped". "Since you last looked" folds these rows as before.
- A project no longer shows the same link twice. A projects-file url that
  repeats a link the project already has (often its GitHub mirror) is left
  out, and the log names it once so the projects file can be tidied.
  Probes are unchanged.
- In the first 10 minutes after a start, while a forge has not listed its
  repos yet, a container whose outage had already counted no longer
  rewrites `state.json` at every Docker feed (a crash loop wrote it about
  20 times in those minutes for nothing).
- The notes of a GitHub release are now the tag's own notes. The release
  workflow wrote the commit message instead (0.2.1 and 0.2.2), because the
  checkout left the tag without its annotation.
- A one-off HTTP 502, 503 or 504, or a refused or dropped connection, no
  longer turns a forge red: Gitea, Forgejo and GitHub reads are tried twice
  more, after about 1 and 3 seconds. A timeout, a 4xx or a rate limit is not
  tried again.
- A forge that stops answering, or still answers 502, 503 or 504 or
  refuses the connection after those tries, costs one failed read per pass
  (at most four, one per read under way at once), not one per repo: its
  other repos wait for the next pass with their last good data, and its
  status says "Gitea not answering; 12 repos not read this pass". A repo
  whose last read failed is read after the others, so one failing repo does
  not hold them up, and while the forge answers its other repos the status
  names that repo rather than saying the forge is not answering. A mirror
  whose read failed for a passing reason (a timeout, a dropped connection
  or a 5xx answer, not a 4xx) is read again after 1, 2, 4 and up to 30
  minutes, not only at the next 30 minute pass.
- Without a GitHub token, mirrors spend less of the 60 reads an hour: a
  repo with no open issues or pull requests is read in two calls, not
  three; after a rate limit only the mirrors it stopped are read again; and
  when an answer says no reads are left, ForgeMap stops asking until the
  reset instead of making the call that would be refused.
- F-Droid: an app on the main f-droid.org repo works. The index (about
  75 MB) is streamed and only the packages the projects file names are
  kept (a package named later is read as soon as the projects file is read
  again); a read fails once the index is over 256 MiB; each read has its
  own 2 minute deadline,
  though a server that has not begun to answer after 20 seconds fails as
  before; and an unchanged index (a 304 answer) is not downloaded again.
- Gitea and Forgejo track any user or org in `owners`, as GitHub does: each
  owner is read from its own list, not filtered out of what the token user
  can see. An org is read from its own list when the token has the
  `read:organization` scope, and from the same list as a user when it does
  not; each owner's list is asked first where it answered last time. Lists
  page on to the server's total, so a server with a smaller page size is no
  longer cut to its first page; a list cut at 40 pages is named in the
  forge's status; and an owner with no repos is logged as having "no repos
  the token can see, or no such owner".
- A forge whose repo list has never worked no longer adds "(showing the last
  good data)": there is none yet.
- One slow or hanging forge no longer holds the others up: each forge is
  listed and read on its own minute, starting a few seconds apart, so a
  forge that answers keeps its data fresh while another times out. This was
  a known limitation of 0.2.0. Local projects and ideas files are read on a
  minute of their own; files in a forge repo with that forge's reads.
- A 404 for a very long address answers at once. The "did you mean" list
  is not worked out for an address over 64 characters, and the page shows
  at most 64 characters of it; a 1 MB address took about 3 seconds of CPU.
  Request headers are capped at 16 KiB, with Go's 4 KiB of slack (a
  larger request gets 431).
- Live updates have limits on a public page. At most 256 live streams are
  open at once; one more is refused, and the page tries again on its own
  after about 5 seconds, waiting twice as long each time, up to a minute.
  A write that waits 30 seconds ends its stream,
  every stream ends after 30 minutes and the browser opens a new one within
  a second (the header does not say "Reconnecting" for it), and an idle
  connection is closed after 2 minutes.
- The overview keeps up while it is open. The tiles, the week strip and
  Needs you update with each change, a card whose state changes moves to
  that state's group with every count updated, and a project added or
  removed appears or goes, all without a reload. It said "Live" while only
  the cards changed.
- The 24 hour uptime bar no longer hides a short outage. A ten-minute
  cell with any failed check shows as down, and its tip counts them, such
  as "1 of 5 checks down". Down cells are full height and striped and up
  cells shorter, so the state shows by shape, not by colour alone. A line
  under the bar says "No failed checks in 24 hours" or counts them and
  names the last one, such as "Failed checks: 3 in 24 hours, last at 03:50".
- In a contrast theme (forced colours, such as Windows Contrast Themes)
  the heatmap, the uptime bar and ring and the status dot no longer
  vanish: they draw in the theme's own colours, with heat levels shown by
  outline weight and failed checks by stripes.

## [0.2.2] - 2026-11-13

### Changed

- A probe counts as down only after 2 failed checks in a row. One failed
  check shows as a warning, "Failed once, checking again", and gives no
  Fix, no Needs you item, no change of state and no event, so a blip no
  longer flaps the Needs you count or fills the activity feed. A confirmed
  outage's Fix and down event are dated from its first failed check. The
  uptime figure and the 24 hour bar still count every check.
- A container that is not running (stopped, restarting, paused or missing)
  counts only once it has been seen so for 2 minutes, by docker's own age
  or the first feed that showed it. Until then the page says so ("Stopped,
  checking again") and nothing else follows, so a one-minute nightly
  restart no longer counts as Needs you. A confirmed stop's Fix and event
  are dated from the stop.
- "Since you last looked" stays short. A project's run of the same change
  folds into one row with its count, such as "9 new commits" or "Live went
  down and came back 3 times"; releases and state changes stay one row
  each. Each project shows at most 3 rows, with the rest under "N more",
  and the block at most 12, every project's first row before any project's
  second. A first visit and the page without JavaScript now get this short
  view of the last 7 days instead of every change in it.
  A return visit counts only the changes since the last look, folded and
  capped the same way for each project, so one busy project no longer
  fills the list.

### Fixed

- `state.json` is written only when something in it changed: events and
  fingerprints within 10 seconds as before, probe points alone at most every
  10 minutes, and everything at shutdown. It was rewritten about twice a
  minute, about 3.3 GB of disk writes a day for a 1 MB file, which wears an
  SSD or SD card. A crash now loses at most 10 minutes of uptime history.
- A restart while a forge has not listed its repos yet (Gitea starting
  after Waymark at a host reboot, or a forge rate-limited at start) no
  longer deletes the 24 hour probe history of the projects and homepages
  only that list shows: probes are pruned only once every forge has listed.
  With `include_private: false`, a project Live through its probe alone no
  longer writes a false "Now Quiet (was Live)" and "Now Live (was Quiet)"
  pair when the forge answers: its tier is not compared until its probe
  has been checked again.
- Live updates come back on their own after a deploy or a restart. When the
  proxy answered with an error (502) while Waymark was down, every open tab
  said "Reconnecting" for ever and kept old data until it was reloaded. It
  now tries again after 5 seconds, then less often, at most once a minute;
  after 2 minutes the header says "Offline, retrying" and the sources list
  offers a Reload link.

## [0.2.1] - 2026-11-01

### Added

- `meta.projects_file` and `meta.ideas_file` in the site JSON say which of
  the two files are configured.

### Changed

- The README is a picture tour: one screenshot and a line or two for each
  page. The detail moved to the GitHub wiki: Installation, Privacy and
  Security, Features, Configuration Reference, and FAQ and Known Limits.
  The privacy list there is unchanged; `docs/configuration.md` stays in the
  repo as the master of the Configuration Reference.
- Plainer words on the pages, using the names the docs use. The map's
  legend and lead say "state", as every other page does. The projects file
  is called that: a Confirm card's source reads "Projects file hold vs Gitea
  commits" (or GitHub, or the forge's own name), a retired project says
  "Retired in the projects file", and the source row for the files is
  named after the ones the site has: "Projects file", "Ideas file" or
  "Projects and ideas files". The Ideas page says "Ideas from the ideas
  file (updated 31 Oct 2026)". JSON field names are unchanged.
- Surprise me keeps its pick marked: after the scroll the idea keeps an
  outline and a "Surprise pick" label until the next pick, Esc or a filter
  change, also when the device asks for less motion. The next pick is never
  the same idea when there is another.

### Fixed

- Surprise me no longer leads to a 404 on a site with no Ideas page: the
  command palette offers it only when there is an idea to pick, and an Ideas
  page with no ideas yet says so instead of showing empty filters, naming
  where ideas come from: the ideas file, labelled issues, or both.
- A site with neither a projects file nor an ideas file no longer shows a
  source row for them.
- A rate-limited forge's status names the time after which Waymark will ask
  it again (at most an hour away), not the forge's own reset; a shorter
  limit met later no longer changes the words while the longer wait stands.
  The heatmap, filed-idea search, mirror and file rows the limit stops name
  the same time.
- `WAYMARK_INCLUDE_PRIVATE` is read, and checked, only when the config file
  has no `include_private`: a bad value no longer fails a config that sets
  the key.
- Mirrors are read again within a minute of a rate limit's end, not at the
  next 30-minute read, so a forge does not stay red for up to 30 minutes
  longer than the limit; with no limit nothing more is asked.

## [0.2.0] - 2026-11-01

The first public release, as Waymark: one codebase anyone can run against
their own Gitea, Forgejo and GitHub accounts, configured by one YAML file or
by environment variables alone.
The Changed and Removed lists below compare it with 0.1.0, a build for one
homelab that was never published.

### Added

- Configuration: one YAML file, `/config/waymark.yml` (`WAYMARK_CONFIG`
  names another path), with `${VAR}` expansion of string values so tokens
  stay in the environment, or an env-only quick start (`GITHUB_TOKEN` and
  `GITHUB_OWNERS`, and/or `GITEA_URL`, `GITEA_TOKEN` and `GITEA_OWNERS`).
  Unknown keys are errors; errors never repeat a token. Every key is in
  `docs/configuration.md`, and `waymark.example.yml` is a complete example.
- Any number of forges, read in config order: Gitea, Forgejo and GitHub.
  Each lists the repos of its `owners`, and each has its own source status.
  Empty `owners` means the token user's own repos only, on every forge type
  (not repos the user collaborates on, nor its organisations' repos); list
  an org or another user to track theirs. An owner with no repos the token
  can see is logged once.
- A full GitHub client: repos per owner, tags, releases, open issues and PRs
  with review requests and assignees, commits in the 84-day window, Actions
  runs, `CHANGELOG.md`, plan files and the contribution calendar. Every GET
  is conditional on the last ETag. A spent rate limit (or a secondary
  limit's `Retry-After`) names the time it resets in the forge's status,
  and the forge is asked nothing until then (at most an hour). A refresh
  lists every forge again, but reads a GitHub repo's detail only when it
  changed or its last read failed; a Gitea forge reads all again, as 0.1.0
  did. Open issue and PR counts come from the repo detail, so a GitHub
  primary's PRs are not counted as issues. An owner that does not exist is
  skipped (and logged once) rather than failing the list.
- Projects from repos: every tracked repo the projects file does not claim
  or hide becomes a project. Repos with the same name on two forges pair
  into one project; the first forge in config order is the primary, and a
  pull mirror is never the primary when its twin is not a mirror. An
  unclaimed repo with the same name as a projects-file project's repo, on a
  forge where that project has none, joins it as a mirror.
- Needs you from the forges: an open issue or PR with a `needs_you.labels`
  label (default `needs-you`, `decision`) or assigned to the token user is a
  Decide item, and a PR asking for the token user's review is a Review
  item, one item per issue or PR.
- Ideas from labelled issues: an open issue carrying one of `ideas.labels`
  (default `idea`, `enhancement`, any case) in a project's own repos becomes
  an idea that links its issue, with kind and size from `kind/*` and
  `size/*` labels. They are taken from each repo's newest open issues (up
  to 10 per repo). A mirror's issues never do. The ideas file is optional;
  "File as issue" stays for its ideas, and an issue filed from one (or named
  as its existing issue) shows once, as that file idea.
- Notes adapters: `notes.type: markdown` reads any folder of `.md` files
  (subfolders too, hidden ones skipped). A file belongs to a project by its
  name (`<slug>.md`, or the slug with `-` as `_`), by frontmatter
  `project: <slug>`, or by the projects file's `notes:` list; `updated:`
  dates it, else the file's mtime. A list item carrying one of
  `notes.markers` (as written, never inside a word: `bob@me.com` is not
  `@me`) is a Decide item with the marker removed; task boxes (`- [ ]`,
  `- [x]`) and fenced code never are. A markdown note's body shows in the
  notes panel: its first 40 lines, the rest behind "N more lines".
- Docker from the Engine API: `docker.host` (`unix:///path` or
  `tcp://host:port`, for a docker-socket-proxy) as an alternative to the
  feed file. One read-only `GET /containers/json?all=1` per poll. A
  container also belongs to the project whose repo page (primary or mirror)
  is its `org.opencontainers.image.source` label; a container named in a
  `containers:` list stays with that project. The client follows no
  redirects and never puts the host's credentials in an error or
  `site.json`.
- Repo homepages are probed (`probes.homepages`, on by default): each repo's
  website, unless a projects-file url already covers it, and never a repo
  left out as private. A homepage shows in "Where it runs" with up/down and
  its 24-hour history, but never makes a project Live, raises Fix, adds a
  chip or an event; only projects-file urls do. A homepage comes from repo
  metadata, so it is only probed at a public address: one that resolves to a
  loopback, private, link-local, unspecified (including 0.0.0.0/8),
  multicast or shared (100.64/10) address is not requested and shows as
  "Not probed". Homepage probes use no HTTP proxy. Projects-file urls may
  point anywhere.
- Demo mode: `-demo` (or `make demo`) serves the real pipeline against an
  in-process fake Gitea and GitHub holding an invented team, `acme`, with
  about ten repos built to show every state (a lagging deploy, CI failing on
  main, a hold, a mirror pair, an empty repo, notes only, a plan in
  progress, needs-you labels, assignments and review requests, ideas, a
  container matched by its OCI label, a probe down, an F-Droid package, an
  archived and a private repo). It needs no config and reaches nothing
  beyond loopback; every page carries a line saying it is demo data.
- `site.json` carries `meta` (the title, the notes panel's label, which
  optional sources the site has, each forge's name, the configured time
  zone, and `demo` for demo data), each project's `forge`, each issue's
  `forge` and `mirror`, and a probe's `homepage` and `not_probed`;
  `/api/live.json` and the live updates carry each source's `label`.
- Projects file: `plan_dirs:` overrides the plan directories per project.
- `include_private` (default `false`) drops private repos at listing time,
  everywhere: projects, needs-you, ideas, events, the ship log and the map.
  `WAYMARK_INCLUDE_PRIVATE` (`true` or `false`) sets it when the file does
  not, and is the only way in the env-only quick start.
- The last good projects and ideas files are cached in the data directory
  and used when their source fails, so a forge that is down at start-up does
  not empty the page.
- Published as images (`ghcr.io/will-luck/waymark`, `willluck/waymark`,
  amd64 and arm64) and as binaries for Linux (amd64, arm64, armv7) and macOS
  (amd64, arm64). The image runs as uid 1000 with `/data` as a volume and
  reads its config from `/config`. A prerelease tag (one with a `-`) is
  published as a prerelease and never moves `latest`.
- Every request names its client: `User-Agent: waymark/<version>` to the
  forges (REST and GraphQL), the F-Droid index and the Docker Engine API,
  `waymark-probe/<version>` for probes.
- `-version` prints `waymark <version>`. At start the log names the config
  it read (`config: <path>` or `config: environment only`) and warns of a
  token whose `${VAR}` is empty or not set, never printing a value.
- `scripts/docker-feed.sh`: the per-minute host cron that writes the
  container feed, so the container never mounts the Docker socket.
  `FEED_DIR` defaults to `/var/lib/waymark-feed`.

### Changed

- The name is Waymark: the binary and image are `waymark`, and the Go module
  is `github.com/Will-Luck/waymark`. The page title is `site.title`
  (default "Waymark").
- Private repos are off by default, since the page has no login: 0.1.0
  showed every repo its token could see. `include_private: true` (or
  `WAYMARK_INCLUDE_PRIVATE=true`) shows them again.
- The page title, header and footer use `site.title`; the notes panel and
  its source row use the notes adapter's label.
- Dates and times on the page are written in `site.timezone`. With no
  `site.timezone` the zone is `$TZ`, or UTC when that is unset; 0.1.0 wrote
  every date in one fixed zone.
- Notes are optional (`notes.type: none` by default). Claude's notes
  (`claude-memory`) wait on the names in `site.you`, matched as written or in
  capitals; with no names nothing in them needs you. A name with a
  non-ASCII letter at either end (José, Zoë) is matched on letter
  boundaries. The source of note actions and the note-updated event come
  from the adapter ("Claude's notes", "Notes").
- A source that is not configured is not shown: no notes panel or note
  events without notes, no Docker part of "Where it runs" without Docker, no
  Ideas page (404), nav link or panel without ideas, and no status row (so
  never "failing") for notes, Docker, ideas or F-Droid when the site has
  none. Before a forge has listed its repos, a project on it says "<forge>
  not read yet" rather than "No commits yet". With no projects file
  configured, a cached copy from an earlier config is not shown.
- Forges are named by kind: links, issue headings, chips, the deploy, review
  and hold sources, and "not read yet" say GitHub for a GitHub forge,
  Forgejo for type `forgejo`, and Codeberg for a Gitea or Forgejo forge on
  codeberg.org; two forges with one name add their id ("Gitea (work)"). The
  source rows and a project's sources use the same names, never the forge
  id. Issues read through the mirror view are marked as such (`mirror`)
  rather than by the word "github".
- Ideas from labels: the Ideas page says they come from labelled issues (and
  from the roundup when there is an ideas file too), shows no kind or size it
  does not know, counts none as filed, and links "Issue #N". Size labels map
  onto Small, Medium and Large: `size/xs`, `size/s` and `size/small` are
  Small, `size/m` and `size/medium` Medium, `size/l`, `size/large` and
  `size/xl` Large (any case); any other size gives none.
- The filed-idea marker is `ideas.marker` (default `waymark-idea:`; 0.1.0
  used `roadmap-idea:`, which a config can keep so issues already filed stay
  linked): the filed-idea search and the "File as issue" body use it. With
  no ideas file there is no filed-idea search and no ideas source.
- The "File as issue" body's closing line is "From the ideas list." followed
  by the marker line.
- Projects file: a `repos:` key is a forge id, and `owner:` only qualifies
  bare repo names.
- The projects and ideas files can be local files or files in a forge repo.
  The time zone, the Docker feed path and its label, and the plan
  directories (`plans.dirs`) come from the config, and `notes.type` decides
  whether notes are read.
- Mirrors get the light read (repo, issues, latest release); a GitHub forge
  without a token stays OK and its projects-file repos are read like
  mirrors. When that leaves its owners unlisted, or nothing to read (no
  projects-file repo on it), its source row says why (`note` in the source
  status) and the log says so once. Heatmaps from every forge with
  `heatmap` on are summed for each token's user.
- A repo the projects file names is read by name when its owner is not in
  `owners` (owners decide the auto projects only); one that is not there is
  named in its forge's status.
- `include_private: false` shows a repo only once it is known to be
  public. A projects-file project shows only once its primary repo is (in
  its forge's list or read by name: not while that forge has not answered,
  never when private: no card, needs-you, events, map node or ship log). A
  mirror is linked and shown only once it is (its forge's list, a by-name
  read, or its own view); one whose view says private is not read again. An
  event shows only when its link is into a repo known to be public, matched
  by that repo's page on its own forge: events stored by an earlier run stay
  out, also after a restart before the forges answer, and a private repo
  hides nothing of a same-named public one on another forge. A filed idea
  is linked only into a repo known to be public, and a forge's status never
  names a repo known to be private. What it cannot hide: a GitHub forge
  with no token cannot know a named repo is private (anonymous reads answer
  404), so such a project shows its projects-file fields, and a mirror named
  on such a forge is linked as named; the heatmap counts
  all of the token user's activity, private repos included (a number per
  day, no names); and a failed read of a projects-file repo whose privacy
  is not known yet is named in its forge's status.
- Events: until every forge has listed its repos once since start, for up
  to 10 minutes, nothing is compared and the stored state is kept, so a
  restart with one forge down (a pair's project then built from its
  mirror) gives no events that are no change. After 10 minutes a forge
  still not listed (a wrong token, or offline) is logged once and taken as
  down: events go on, and the projects only it builds keep their last
  state. A pair whose primary is on that forge is built from its mirror
  meanwhile, so it can give a few events at the 10-minute mark and again
  when the forge returns.
- Config checks: an unknown `site.timezone`, an empty `notes.dir` for
  markdown or claude-memory notes, a forge id that names another source,
  a forge `url` that is not `http://` or `https://`, holds a user name or
  password, or ends in `/api/v1`, and a `docker.host` that is not
  `unix:///path` or `tcp://host:port` (or carries credentials) are errors.
  In the env-only quick start an error names the variable (`GITEA_URL`).

### Fixed

- Both notes types read only inside the notes folder (which may itself be a
  symlink): a symlink that leaves it, and an absolute symlink, is not
  followed, only regular files are read, a file over 1 MiB is skipped and
  logged once, and a note body keeps at most 64 KiB.

### Removed

- The built-in owner, Gitea address, time zone, docs-repo plan rule and
  copies of the projects and ideas files: each is now config, or the cached
  last good copy.
- The 0.1.0 environment variables `ROADMAP_LISTEN`, `FEED_DIR` and
  `NOTES_DIR`: use `WAYMARK_LISTEN` (or `listen`), `docker.feed` and
  `notes.dir`.

### Known limitations

- One slow forge delays the others' refresh: they are read in one cycle, though each call is bounded by its timeout.
- Forks of other people's repos become projects; hide them with `hide_repos` in a projects file.
- Homepage probes do not refuse NAT64, benchmarking or other reserved ranges; they report only up or down and the latency.
- A forge that has not listed its repos (a wrong token, or offline) holds back new events for up to 10 minutes after start.

## [0.1.0] - 2026-11-01

0.1.0 was an internal build and was not published.

# Configuration

ForgeMap reads one YAML file. With no file it runs from environment variables
alone (the [quick start](#environment-only-quick-start)), and with neither it
is [set up in the browser](#set-up-in-the-browser). Everything here is
optional except at least one forge.

- [Where the config comes from](#where-the-config-comes-from)
- [Environment variables](#environment-variables)
- [Command-line flags](#command-line-flags)
- [Set up in the browser](#set-up-in-the-browser)
- [The config file](#the-config-file): [`site`](#site),
  [`include_private`](#include_private), [`refresh`](#refresh), [`forges`](#forges),
  [`projects`](#projects), [`ideas`](#ideas), [`needs_you`](#needs_you),
  [`notes`](#notes), [`plans`](#plans), [`docker`](#docker),
  [`probes`](#probes), [`listen` and `data_dir`](#listen-and-data_dir)
- [The projects file](#the-projects-file)
- [The ideas file](#the-ideas-file)
- [Notes folders](#notes-folders)
- [Plans and changelogs](#plans-and-changelogs)
- [The Docker feed](#the-docker-feed)
- [The activity feed](#the-activity-feed)

A complete example is [`forgemap.example.yml`](../forgemap.example.yml).

## Where the config comes from

1. The file named by `FORGEMAP_CONFIG`, if it is set.
2. Otherwise `/config/forgemap.yml`, if it exists. In Docker, bind-mount a
   directory there read-only: `-v "$PWD/config:/config:ro"`.
3. Otherwise, when `GITEA_URL`, `GITEA_TOKEN`, `GITHUB_TOKEN` or
   `GITHUB_OWNERS` is set, the [environment-only quick
   start](#environment-only-quick-start).
4. Otherwise [settings made in the browser](#set-up-in-the-browser), kept in
   `data_dir`.

A config file or the quick start does not start until the config is valid.
An unknown key is an error that names the key. Errors never repeat a token.

### `${VAR}` expansion

`${VAR}` and `$VAR` in the file are replaced from the environment, so tokens
stay out of it: `token: ${GITEA_TOKEN}`. A variable that is not set becomes
an empty string.

- Expansion runs on string values only, after the YAML is read: a variable
  cannot add keys, and comments are never expanded. A non-string field cannot
  take a variable (`heatmap: ${H}` fails to load).
- In a flow mapping, quote the value: `{token: "${T}"}`. Unquoted, YAML reads
  `${T}` as the start of another mapping.
- There is no escape: a `$` followed by a letter, a digit, `_`, `{` or one
  of `*#$@!?-` always starts a variable (`$_x` is the variable `_x`). Keep a value that holds a `$` in the
  environment and write `${NAME}` in the file.

## Environment variables

| Variable | Used for | Default |
|---|---|---|
| `FORGEMAP_CONFIG` | Path of the config file | `/config/forgemap.yml` when it exists |
| `FORGEMAP_LISTEN` | Address to serve on, when the file has no `listen` | `:8080` |
| `DATA_DIR` | State directory, when the file has no `data_dir` | `/data` |
| `FORGEMAP_TITLE` | Page title, when the file has no `site.title` | `ForgeMap` |
| `TZ` | Time zone, when the file has no `site.timezone` | `UTC` |
| `FORGEMAP_INCLUDE_PRIVATE` | `true` or `false`: show private repos, when the file has no `include_private` (the only way in the quick start) | `false` |
| `FORGEMAP_FORKS` | Quick start only: `true` or `false`, track forks too (a file sets `forges[].forks` per forge) | `false` |
| `FORGEMAP_REFRESH` | `true` or `false`: offer Refresh now and `POST /api/refresh`, when the file has no `refresh` | `true` |
| `GITEA_URL` | Quick start only: your Gitea or Forgejo address | none |
| `GITEA_TOKEN` | Quick start only: its read-only token | none |
| `GITEA_OWNERS` | Quick start only: comma-separated users and orgs to track | the token user's own repos only |
| `GITHUB_TOKEN` | Quick start only: a read-only GitHub token | none |
| `GITHUB_OWNERS` | Quick start only: comma-separated users and orgs to track | the token user's own repos only |

The quick-start variables are read only when there is no config file. With a
file, name them yourself: `token: ${GITHUB_TOKEN}`.

### Environment-only quick start

With no config file, ForgeMap makes up to two forges from the environment:

- `GITEA_URL` or `GITEA_TOKEN` set: a forge with id `gitea`, type `gitea`,
  owners from `GITEA_OWNERS`, heatmap on.
- `GITHUB_TOKEN` or `GITHUB_OWNERS` set: a forge with id `github`, type
  `github`, owners from `GITHUB_OWNERS`, heatmap on.

Every other setting takes its default: no projects file, no ideas file, no
notes, no Docker, homepages probed, private repos and forks left out
(`FORGEMAP_INCLUDE_PRIVATE=true` and `FORGEMAP_FORKS=true` show them). Each
tracked repo becomes a project; a repo with the same name on both forges is
one project, with Gitea first.

## Command-line flags

| Flag | What it does |
|---|---|
| `-listen <addr>` | Serve on this address; wins over `listen` and `FORGEMAP_LISTEN` |
| `-demo` | Serve an invented team from a built-in fake forge: no config, no network, every page says it is demo data |
| `-dump <path>` | Collect once, write the assembled site JSON to the path and exit |
| `-fixture <path>` | Serve a saved site JSON file (no sources are read); an edit to the file is served within a second and sent to open pages as a live update |
| `-version` | Print `forgemap <version>` and exit |
| `-reset-password` | Set up in the browser only: a new random password for the account; every browser is signed out; prints the password and a one-time sign-in link (it works once, for 15 minutes) and exits |

At start the log says which config it read (`config: /config/forgemap.yml`,
`config: environment only`, or `config: settings in /data (set up in the
browser)`), and warns of a token whose `${VAR}` is
empty or not set; it never prints a value.

## Set up in the browser

With no config file and none of the four quick-start forge variables, a new
install is set up in the browser instead.

- **When it applies:** the data dir is new. None of `settings.json`, or of
  `state.json`, `projects.cache.yaml` and `ideas.cache.json`, or a file
  whose name starts with one of those three (a left-over copy), is in it. A
  data dir already in use with no `settings.json` (a config mount gone
  missing, say) stops the start as before, and the log adds how to set up
  in the browser instead: start with an empty data volume.
- **Which data dir:** `DATA_DIR`, else `/data`. A `data_dir` set inside a
  config file is not seen when that file is missing, so the check looks at
  `DATA_DIR` or `/data`. The user ForgeMap runs as (uid 1000 in the image)
  must be able to write it: until setup is finished, a data dir it cannot
  write stops the start, and the log says why. Once set up, the page is
  still served from one it cannot write, and the log warns that no change
  is saved.
- **The environment:** `FORGEMAP_LISTEN` and `DATA_DIR` still apply. `TZ` and
  `FORGEMAP_TITLE` only fill in the setup form. `FORGEMAP_INCLUDE_PRIVATE`,
  `FORGEMAP_REFRESH` and `FORGEMAP_FORKS` are not read; the log warns once
  for each one that is set.
- **The setup window:** a start that finds no account keeps setup open
  for 5 minutes, and the log says until when (`setup: open the page and
  make your account within 5 minutes, by 14:05:09. A restart opens setup
  again.`). During those minutes anyone who can open the page, or a web
  page open in a browser on your network, can make the account: start
  ForgeMap when you are ready to set it up, and do not expose a new
  install to the internet before the account exists. Once the window has
  closed with no account, the page says so; restart ForgeMap to open it
  again. If a new install shows anything but setup before you have made
  an account (it asks you to sign in, or shows a page), someone else made
  it first: stop ForgeMap, empty the data volume and start again. `-reset-password` alone does not undo it, because it keeps
  the feed key and the saved settings: after it, press **Make a new key** in
  Settings and check each forge and who can see the page.
- **Settings** changes every key on this page but `listen` and `data_dir`,
  and applies it live, without a restart. A forge's id is generated, never
  edited, and is its name in a projects file.
- **Who can see the page:** with "Only me, after signing in" in setup, or
  "Ask for sign-in to see the page" in Settings (on by default), the pages,
  the JSON API and the feed need the account. Settings always does.
- **The feed key:** `fm_` and 32 characters, for feed readers and scripts.
  It is accepted as `?key=`, as `Authorization: Bearer <key>`, or as Basic
  auth with any user name and the key as the password, on `/feed.atom`,
  `/api/*.json` and `POST /api/refresh` only, so a push webhook keeps
  working. **Make a new key** in Settings stops the old one at once.
- **`settings.json`** is mode 0600. It holds the forge tokens **in plain
  text**, the password hash (PBKDF2-SHA256, 600,000 rounds), the two signing
  secrets and the feed key (and, after `-reset-password`, the SHA-256 of its
  one-time link, never the link, until the link is used or replaced or the
  password is changed; the link stops working after 15 minutes): treat a
  copy of the volume as a secret. Never edit it by hand; a file that cannot be read stops the
  start, and it is never moved aside.
- **A lost password:** `forgemap -reset-password` prints a new one; in
  Docker, `docker exec forgemap forgemap -reset-password`. Run it as the
  user ForgeMap runs as. Every browser is signed out, and the running server
  takes it at the next sign-in. The feed key and the settings stay as they
  were, and it says so. It also prints a one-time link,
  `/signin?once=<token>`, to add to your ForgeMap address: when others'
  wrong guesses have the sign-in page saying there were too many tries, it
  lets one sign-in skip the wait. It works once, for 15 minutes, and still
  needs the new password.
- **Behind a reverse proxy:** the proxy must pass the browser's `Host` (or
  set `X-Forwarded-Host`) and set `X-Forwarded-Proto`, or every form is
  refused as sent from another site.
- **Plain HTTP** sends the password across the network in clear: use HTTPS,
  or keep the page on your LAN or VPN.
- **One address, several apps:** cookies are shared across the ports of one
  host name, so give ForgeMap a host name of its own.
- **Forge requests** ignore `HTTP_PROXY` and `HTTPS_PROXY` and follow no
  redirect. Each forge is tested with its token before it is saved.
- **A forge with a self-signed certificate** (or one made for another
  name, or out of date): **Connect** shows the certificate, with who it
  was issued to and by, its last valid day and its fingerprint. Tick
  **Trust this certificate for this forge** and ForgeMap accepts that
  certificate, and no other, from that address. The forge's page in
  Settings shows the trusted fingerprint and **Stop trusting**; when the
  forge's certificate changes, the overview says so and the forge's page
  shows the new one to check and trust. Replacing the token of a forge
  that has moved to a certificate the normal check accepts also stops
  trusting the old one. GitHub is always checked the normal way. A config-file install has no such setting, nor has GitHub:
  mount the CA and set `SSL_CERT_FILE`, as the install guide's
  "Self-signed certificates" section shows.

## The config file

Each table lists the key's full path, its type, its default and what it
does. A list key left out takes the default; an empty list (`[]`) turns it
off.

### site

| Key | Type | Default | What it does |
|---|---|---|---|
| `site.title` | string | `ForgeMap` (or `FORGEMAP_TITLE`) | The page title, header and footer |
| `site.timezone` | string | `$TZ`, else `UTC` | An IANA zone such as `Europe/Paris`. Every date and time on the page is written in it, and "today" and the week boundaries follow it |
| `site.you` | list of strings | `[]` | Your names as notes write them. Only the `claude-memory` notes type reads it; with no names, nothing in those notes waits on you |

```yaml
site:
  title: Team status
  timezone: America/New_York
  you: [Mark]
```

### include_private

| Key | Type | Default | What it does |
|---|---|---|---|
| `include_private` | bool | `false` (or `FORGEMAP_INCLUDE_PRIVATE`) | `false` drops private repos everywhere: projects, needs-you, ideas, events, the ship log and the map. `true` shows them like any other repo |

A config file or quick-start install has no sign-in, so private repos are
off unless you turn them on. With `true`, everything the [privacy list](https://github.com/Will-Luck/forgemap/wiki/Privacy-and-Security#who-can-see-the-page)
names shows for private repos too (names, issue titles, commit messages),
to anyone who can reach the page: put a login in front of it first.

Set up in the browser, **Show private repos** works only while the page asks
for sign-in, and turning sign-in off turns it off.

With `false`, a repo shows only once it is known to be public:

- A projects-file project shows only once its primary repo is (in its
  forge's list, or read by name), so while that forge has not answered it
  stays hidden. A mirror is linked and shown only once it is (its forge's
  list, a by-name read, or its own view); one whose view says private is
  not read again. The exception is a mirror on a GitHub forge with no
  token: GitHub answers 404 to anonymous reads of a private repo, so that
  mirror is linked as the projects file names it.
- An event shows only when its link is into a repo known to be public, so
  events stored by an earlier run with private repos stay out, also after a
  restart before the forges answer.
- A forge's status never names a repo known to be private. A failed read of
  a repo the projects file names, while its privacy is not known yet, is
  named (it is your file's name for it).
- A GitHub forge with no token cannot tell a named repo is private (GitHub
  answers 404 to anonymous reads), so such a project shows only what the
  projects file says about it.
- The heatmap counts all of the token user's activity, private repos
  included: a number per day, with no repo names.
- When every repo a forge lists is private, its source row says "Private
  repos are hidden (include_private is off)". That tells anyone who can see
  the page that private repos exist, with no names and no count (the log
  has the count).

### refresh

| Key | Type | Default | What it does |
|---|---|---|---|
| `refresh` | bool | `true` (or `FORGEMAP_REFRESH`) | `true` offers **Refresh now** in the command palette and answers `POST /api/refresh` (a forge's push webhook can call it). `false` takes Refresh now out of the palette, and `POST /api/refresh` answers 404 |

A refresh is an early run of the usual reads, at most one a minute: every
forge is listed again and the projects and ideas files are read again, and a
repo's detail is read only where its list entry changed or its last read
failed, on every forge type. Mirrors read in the last 5 minutes, and the
heatmap, the filed-idea search, the F-Droid indexes and the probes read in the
last 2 minutes, wait for their own turn; a forge whose rate limit is spent is
asked nothing. So on a world where nothing changed a refresh costs one repo
list per forge.

`POST /api/refresh` answers 202 with `{"ok": true, "queued": true}`, or, when
one was queued less than a minute ago, 200 with `{"ok": true, "queued": false,
"last_at": "...", "next_at": "..."}` (RFC 3339 times): the last was queued at
`last_at`, and the next can start at `next_at`.

### forges

A list, in order. The first forge holds the primary repo when the same repo
name is on two forges; the others are mirrors (stars, mirror issues, latest
release).

| Key | Type | Default | What it does |
|---|---|---|---|
| `forges[].id` | string | required | Your name for the forge. The projects file uses it as a repo key (`- gitea: acme/tool`), and it names the forge's source status. Unique, and not one of `registry`, `heatmap`, `ideas`, `docker`, `probes`, `notes`, `fdroid` |
| `forges[].type` | string | required | `gitea`, `forgejo` (the same API) or `github`. The page names the forge by it (Gitea, Forgejo, GitHub), or Codeberg for one on `codeberg.org` |
| `forges[].url` | string | required for `gitea`; `https://api.github.com` for `github` | Gitea or Forgejo: the site address, such as `https://git.example.com` (ForgeMap adds `/api/v1`, so leave it off). GitHub: the API root. An `http://` or `https://` address with no user name or password (the token goes in `token`). GitHub Enterprise Server is not supported yet: ForgeMap sends GraphQL to `<url>/graphql`, which it serves elsewhere |
| `forges[].token` | string | none | A read-only token (see [Tokens and scopes](https://github.com/Will-Luck/forgemap/wiki/Installation#tokens-and-scopes)). **Gitea and Forgejo** need one: without it their API answers 401 and the forge's source shows as failing. **GitHub** runs without one but lists nothing: only repos the projects file names are read, and only lightly (the repo, its open issues when it has some, and its latest release; 60 requests an hour). Its source stays OK, and says why when that leaves `owners` unlisted or nothing to read. A `${VAR}` here that is empty or not set is warned of at start |
| `forges[].owners` | list of strings | `[]` | The users and orgs whose repos are tracked; each repo becomes a project unless the projects file claims or hides it. Empty means the token user's own repos only, on every forge type: not repos the user collaborates on, and not its organisations' repos. List an org or another user to track theirs (and list yourself too, to keep your own); Gitea and Forgejo read each owner's own list, as GitHub does, so this is any user or org, not only one the token user belongs to (an org is read from its own list when the token has the read:organization scope, and from the same list as a user when it does not). An owner with no repos the token can see, or no such owner, is logged once. A Gitea or Forgejo list longer than 40 pages (2,000 repos) stops there, and the forge's status says so. A repo the projects file names is read even when its owner is not listed |
| `forges[].heatmap` | bool | `true` | Add the token user's contribution days to the ship log's year of commits: Gitea's heatmap, or GitHub's contribution calendar (needs a token). It counts all of that user's activity, private repos included, whatever `include_private` says: a number per day, no repo names. Turn it off for a forge that mirrors another, or mirrored commits count twice |
| `forges[].forks` | bool | `false` | Track repos that are forks. Off, a fork is tracked only when the projects file names it in a project's `repos:`, so forks made to send one change do not fill the page |

```yaml
forges:
  - id: gitea
    type: forgejo
    url: https://git.example.com
    token: ${GITEA_TOKEN}
    owners: [acme, acme-labs]
  - id: github
    type: github
    token: ${GITHUB_TOKEN}
    owners: [acme]
    heatmap: false
```

What a forge reads:

- **The repo list**, every minute. Each forge is listed and read on a minute
  of its own, a few seconds apart from the others, so a forge that hangs
  holds up only itself.
- **A primary repo's detail**: its tags, releases, open issues and PRs (with
  assignees and review requests), commits from the last 84 days and Actions
  runs, when the repo changes and at least every 30 minutes. With a GitHub
  token, a repo whose only news is a run in progress has only its runs read
  each minute, and its whole detail once the run ends.
- **A primary repo's files**: its `CHANGELOG.md` and plan files, when the
  repo changes and at least every 6 hours.
- **A mirror**: two or three reads every 30 minutes: the repo, its open
  issues (only when it has some) and its latest release.

An unchanged repo comes due on a minute of its own in the last quarter of
each span, so a forge is never asked for every repo at once.

When a read fails: one that fails fast (HTTP 502, 503 or 504, or a refused
or dropped connection) is tried twice more, after about 1 and 3 seconds.
When a read times out, or still fails fast after those tries, the reads
already under way end (at most four at once) and the forge's other repos
wait for the next pass with their last good data. Its status says it is
not answering and how many repos were not read. When the forge answers
its other repos, the status instead counts the repos that could not be
read, says how many more the stop left unread, and names the first
failure; it keeps doing so while that repo fails on its own. A repo whose
last read failed is read after the others, so one failing repo does not
hold them up. A mirror read that a rate limit stopped is tried again
within a minute of the limit's end; one that failed for a reason that
passes (a timeout, a dropped connection or a 5xx answer) after 1, 2, 4 and
up to 30 minutes, but not after a 4xx answer such as a repo that is not
there.

Without a token every GitHub read counts against the 60 an hour GitHub
allows your address, even an unchanged (304) answer, and other programs on
that address share them. When an answer says none are left, ForgeMap asks
nothing more until the reset it names. A fine-grained token with read-only
access to public repositories makes unchanged answers free and allows 5,000
an hour; with a token the forge also lists its `owners` (the token user's
own repos when `owners` is empty).

A [refresh](#refresh) lists every forge again and reads a repo's detail only
where its list entry changed or its last read failed. A forge that answers
that its rate limit is spent (GitHub) is asked nothing until the reset it
names, at most an hour. Its status, and the row of any other read the limit
stops (the heatmap, the filed-idea search, a projects or ideas file in a
repo on it), names the time after which it will be asked again.

### projects

Where the [projects file](#the-projects-file) is: a file on disk, or a file
in a forge repo. Set `projects.file`, or all three of `projects.forge`,
`projects.repo` and `projects.path`; not both.

| Key | Type | Default | What it does |
|---|---|---|---|
| `projects.file` | string | none | Path of the projects file, such as `/config/projects.yaml` |
| `projects.forge` | string | none | A `forges[].id` to read the file through |
| `projects.repo` | string | none | `owner/name` of the repo holding it |
| `projects.path` | string | none | The file's path in that repo, on its default branch |

```yaml
projects:
  file: /config/projects.yaml
# or
projects: {forge: gitea, repo: acme/forgemap-config, path: projects.yaml}
```

The file is read again when it changes (a local file's modification time, or
the repo's last update in its forge's list). The last good copy is kept in
`data_dir` and used when the file cannot be read, so a forge that is down at
start-up does not empty the page. A file that does not parse keeps the last
good copy and says why in the source status.

### ideas

| Key | Type | Default | What it does |
|---|---|---|---|
| `ideas.labels` | list of strings | `[idea, enhancement]` | Open issues carrying one of these labels (any case) in a project's own repos become ideas. A mirror's issues never do |
| `ideas.file` | string | none | Path of an optional [ideas file](#the-ideas-file) |
| `ideas.forge` | string | none | Or read the ideas file through this forge id |
| `ideas.repo` | string | none | `owner/name` of the repo holding it |
| `ideas.path` | string | none | Its path in that repo |
| `ideas.marker` | string | `forgemap-idea:` | The line "File as issue" puts last in a new issue's body, followed by the idea's id; ForgeMap searches for it to find issues already filed. It needs a letter or a digit |

```yaml
ideas:
  labels: [idea]
  file: /config/ideas.json
  marker: "forgemap-idea:"
```

Quote the marker: an unquoted value that ends in `:` is read as a mapping
and the file fails to load. Label ideas come from each repo's 10 newest open
issues, the ones ForgeMap already reads. Finding filed ideas on GitHub needs a
token (it uses the issue search).

### needs_you

The forge rules for the **Needs you** list. The other rules (Deploy, Fix,
stale PRs, Confirm) are always on; see [Needs you](https://github.com/Will-Luck/forgemap/wiki/Features#needs-you).

| Key | Type | Default | What it does |
|---|---|---|---|
| `needs_you.labels` | list of strings | `[needs-you, decision]` | An open issue or PR with one of these labels (any case) is a **Decide** item. Mirror issues count too |
| `needs_you.assigned` | bool | `true` | An open issue or PR in a primary repo assigned to the token user is a **Decide** item. Turn it off when one account owns everything |
| `needs_you.review_requests` | bool | `true` | An open PR in a primary repo that asks for the token user's review is a **Review** item |

Each issue or PR gives one item: a label beats an assignment, which beats a
review request.

### notes

| Key | Type | Default | What it does |
|---|---|---|---|
| `notes.type` | string | `none` | `none` hides the notes panel and note events. `markdown` reads a folder of Markdown files. `claude-memory` reads a Claude Code memory folder. See [Notes folders](#notes-folders) |
| `notes.dir` | string | none | The folder; required for `markdown` and `claude-memory`. Mount it read-only |
| `notes.markers` | list of strings | `["WAITING:", "@me"]` | `markdown` only: a list item containing one of these, as written and never inside a word, is a **Decide** item with the marker removed |

```yaml
notes:
  type: markdown
  dir: /notes
  markers: ["WAITING:", "@me"]
```

### plans

| Key | Type | Default | What it does |
|---|---|---|---|
| `plans.dirs` | list of strings | `[docs/plans, docs/superpowers/plans]` | Folders in each primary repo that hold dated plan files. A project's `plan_dirs` in the projects file overrides it. See [Plans and changelogs](#plans-and-changelogs) |

### docker

Set `docker.feed` or `docker.host`, not both. With neither, the Docker part
of "Where it runs" and the Deploy rule are off.

| Key | Type | Default | What it does |
|---|---|---|---|
| `docker.feed` | string | none | Path of a feed file a host cron writes (see [The Docker feed](#the-docker-feed)), such as `/feed/docker.json`. Read every 30 seconds; flagged when older than 5 minutes |
| `docker.host` | string | none | The Docker Engine API: `unix:///var/run/docker.sock` or `tcp://socket-proxy:2375`. No credentials, path or query. One `GET /containers/json?all=1` every 30 seconds |
| `docker.label` | string | `Docker` | What "Where it runs" calls the host, such as `web-01` |

```yaml
docker:
  host: tcp://socket-proxy:2375
  label: web-01
```

A project with a `containers:` list shows exactly those containers. Any
other project gets the container named exactly like its slug **and** every
container whose `org.opencontainers.image.source` label is the web page of
one of its repos (primary or mirror). A container named in a project's list
belongs only to that project; any other belongs to the first project that
matches it. The first is the project's main container: the slug one, else a
running one. The running version is the image tag, or the `org.opencontainers.image.version`
label when the tag is not a version (such as `latest`).

### probes

| Key | Type | Default | What it does |
|---|---|---|---|
| `probes.homepages` | bool | `true` | Also probe each repo's website (homepage) field. A homepage shows its state and 24-hour history in "Where it runs", but never makes a project Live, raises Fix or adds an event |

A homepage comes from repo metadata, which anyone with write access to the
repo can change, so it is only probed at a public address. A name that
resolves to a loopback, private (RFC 1918, IPv6 ULA), link-local, CGNAT
(100.64.0.0/10), unspecified (0.0.0.0/8, `::`) or multicast address (IPv4,
IPv6 or IPv4-mapped) is not requested, and shows as "Not probed". Homepage probes do not use `HTTP_PROXY` or `HTTPS_PROXY`. The
projects file's `urls:` probes are yours and are requested as given.

A probe is one GET every 2 minutes with a 5-second limit. A 2xx or 3xx answer
(redirects are not followed), 401 or 403 counts as up. A probe counts as down
after 2 failed checks in a row, at most 5 minutes apart; after a longer gap
(a long restart, a sleeping host) the next failed check counts as the first
again, unless the probe had already counted as down. One failed check shows
as "Failed once, checking again" and raises nothing. Uptime and the 24-hour
bar count every check.

### listen and data_dir

| Key | Type | Default | What it does |
|---|---|---|---|
| `listen` | string | `FORGEMAP_LISTEN`, else `:8080` | The address to serve on. `-listen` wins |
| `data_dir` | string | `DATA_DIR`, else `/data` | Where ForgeMap keeps `state.json` (mode 0600: events for 90 days, probe history, each project's last state) and the last good projects and ideas files. Set up in the browser, also `settings.json` (see [Set up in the browser](#set-up-in-the-browser)). The image declares it a volume |

## The projects file

Optional. Every tracked repo becomes a project without it; the file names
projects, groups repos, and adds what a forge cannot know. It is identity,
never state: nothing in it says how a project is doing. Unknown keys are
errors.

```yaml
owner: acme
categories:
  - {key: services, label: Services, icon: server, hue: 150}
  - {key: apps, label: Apps, icon: layout-dashboard, hue: 210}
hide_repos: [forgemap-config, scratch]
projects:
  - slug: beacon
    name: Beacon
    category: services
    icon: bell-ring
    summary: Status pages and uptime alerts for small teams.
    what: Beacon checks the team's sites every minute and posts incidents to a public status page.
    stack: [Go, PostgreSQL]
    runs_on: Docker on web-01
    repos:
      - gitea: beacon
      - github: acme/beacon
    containers: [beacon, beacon-worker]
    urls:
      - {label: Live, url: "https://status.example.com", probe: "https://status.example.com/healthz"}
    notes: [beacon.md]
    relations:
      - {to: atlas, kind: monitors}
    plan_dirs: [docs/plans]
  - slug: inkwell
    name: Inkwell
    category: apps
    repos:
      - gitea: inkwell
    hold: {kind: parked, since: "2026-09-21", reason: Waiting for the new editor library}
```

| Key | Type | What it does |
|---|---|---|
| `owner` | string | Owner for repo names written without one: `- gitea: beacon` means `acme/beacon` |
| `categories[].key` | string | The id projects use in `category`. `unsorted` is taken: it is the category of every project without one, and always comes last |
| `categories[].label` | string | The name shown; defaults to the key |
| `categories[].icon` | string | An icon name from the built-in set (`web/spritegen/icons.txt`, Lucide names) |
| `categories[].hue` | number | The category's colour as a hue, 0 to 360 |
| `hide_repos` | list of strings | Repo names (no owner, any case, any forge) that never become projects. A repo a project names is still read |
| `projects[].slug` | string | Required and unique: lower-case letters, digits and dashes. The page is `/p/<slug>` |
| `projects[].name` | string | Shown name; defaults to the slug |
| `projects[].category` | string | A `categories[].key`. An unknown key files the project under Unsorted, with a warning in the log |
| `projects[].icon` | string | Defaults to the category's icon |
| `projects[].summary` | string | One line for the card |
| `projects[].what` | string | A paragraph for the project page |
| `projects[].stack` | list of strings | Languages and tools, shown as tags |
| `projects[].runs_on` | string | Where it runs, in words, for "Where it runs" |
| `projects[].repos` | list | One-key entries, `- <forge id>: owner/name`. The first is the primary (tags, releases, CI, changelog, plans); a repo on another forge is a mirror; more repos on the primary's forge are read in full too |
| `projects[].containers` | list of strings | Container names for this project, instead of matching by slug or label. A name Docker does not know shows as missing |
| `projects[].fdroid.index` | string | URL of an F-Droid repo's `index-v1.json`, the main repo's included (`https://f-droid.org/repo/index-v1.json`, about 75 MB). Read every 10 minutes and streamed, keeping only the packages the projects file names (at most 256 MiB is read). A read has 2 minutes, a server that has not begun to answer after 20 seconds fails, and an unchanged index (a 304 answer) is not downloaded again. A package named later is read as soon as the projects file is read again |
| `projects[].fdroid.package` | string | The app's package id in that index. Its published version is the running version for the Deploy rule |
| `projects[].urls[].label` | string | The link's name, such as `Live` |
| `projects[].urls[].url` | string | The link |
| `projects[].urls[].probe` | string | A URL to probe every 2 minutes. A probe down for 2 checks in a row (at most 5 minutes apart) raises **Fix** and an up one makes the project Live. Leave it out for a link only |
| `projects[].notes` | list of strings | Note file names for this project (both notes types); a listed file that is missing is reported |
| `projects[].relations[].to` | string | Another project's slug |
| `projects[].relations[].kind` | string | `depends-on`, `client-of`, `fork-of`, `deploys`, `sibling`, `monitors`, `supersedes`, or any word (shown with dashes as spaces). Shown on both projects and drawn on the map |
| `projects[].hold.kind` | string | `parked` or `frozen`: on purpose, not abandoned |
| `projects[].hold.since` | string | `YYYY-MM-DD`, quoted. A commit after it raises **Confirm** ("Still parked?") |
| `projects[].hold.reason` | string | Shown with the hold |
| `projects[].retired` | bool | Shown as Archived. A repo archived on its forge is Archived too |
| `projects[].plan_dirs` | list of strings | Plan folders for this project, instead of `plans.dirs` |

A repo with the same name as a project's repo on another forge, that no
project names, joins that project as a mirror when the project has no repo
on that forge. To keep it apart, name it in another project or hide it.

## The ideas file

Optional JSON, for ideas that are not issues yet. Each shows on the Ideas
page with "File as issue", a link to a new issue in the project's primary
repo, filled in, whose last line is `ideas.marker` and the idea's id. Once
filed, the idea links its issue instead.

```json
{
  "roundup_date": "2026-10-30",
  "ideas": [
    {
      "id": "beacon-01",
      "project": "beacon",
      "title": "Status badge for READMEs",
      "pitch": "An SVG badge with the current status.",
      "why": "Asked for twice this month.",
      "size": "S",
      "kind": "quick-win",
      "existing_issue": 0
    }
  ]
}
```

| Key | Type | What it does |
|---|---|---|
| `roundup_date` | string | `YYYY-MM-DD` the list was written, shown on the Ideas page |
| `ideas[].id` | string | Required and unique; the filed issue is found by it |
| `ideas[].project` | string | A project slug. An idea for a project that is not on the page is left out, with a warning |
| `ideas[].title` | string | The idea, and the new issue's title |
| `ideas[].pitch` | string | A sentence or two; the issue body starts with it |
| `ideas[].why` | string | Why it matters; added to the issue body |
| `ideas[].size` | string | `S`, `M` or `L`; anything else is Unsized |
| `ideas[].kind` | string | `quick-win`, `bold`, `connective` or `hardening`; anything else is shown as Idea |
| `ideas[].existing_issue` | number | An issue number in the primary repo that already covers it; no "File as issue" then |

## Notes folders

Both types read only inside `notes.dir` (which may itself be a symlink). A
symlink that leaves the folder, or an absolute symlink, is not followed;
only regular files are read; a file over 1 MiB is skipped and logged once;
a note's body keeps at most 64 KiB. A project shows at most 4 note items in
**Needs you**.

### markdown

Any folder of `.md` files, subfolders included (hidden ones skipped): plain
notes, an Obsidian vault, a docs folder. A file belongs to a project by its
name (`<slug>.md`, or the slug with `-` as `_`), by front matter
`project: <slug>`, or by the projects file's `notes:` list. Any of these, so
one file can belong to several projects.

```markdown
---
project: beacon
updated: 2026-10-30
---
# Beacon

- WAITING: the hosting quote before we move the database
- [ ] a to-do item is never a needs-you item
```

- **Dated** by front matter `updated:` (`2026-10-30`, `2026-10-30 14:05`, or
  RFC 3339), else the file's modification time. Old notes show their age.
- **Decide items:** each list item containing a `notes.markers` marker, with
  the marker removed. Task boxes (`- [ ]`, `- [x]`) and fenced code never
  are. A marker right after Markdown punctuation (`**@me**`, `(@me)`) does
  not match.
- **Shown** in the project's Notes panel: the first 40 lines of the body,
  the rest behind "N more lines".

### claude-memory

For a Claude Code memory folder: a `MEMORY.md` index of links
(`- [Beacon](beacon.md): deploy waits on Mark`) and one file per note, with
front matter (`name`, `description`, `modified`). Most people want
`markdown` or `none`.

- A project's notes are the files its `notes:` list names, else
  `<slug with - as _>.md`. Only the folder's top level is read.
- **Decide items:** a clause in the note's `MEMORY.md` line, or in its
  newest dated heading (`## 2026-10-30 ...`), that waits on a name in
  `site.you`: "awaits Mark", "needs Mark", "for Mark", "Mark's decision"
  (or call, approval, sign-off, go-ahead, tests). A name matches as written
  or in capitals, never in lower case, so "we will mark it" is not one.
- The panel is called "Claude's notes" and shows each note's index line.

## Plans and changelogs

**Plans.** In each primary repo, ForgeMap looks in the plan folders for
Markdown files whose names start with a date, `2026-10-28-new-editor.md`, or
dated folders holding a `plan.md`. A plan shows under "Next up" with its
first heading and its checkboxes counted ("3 of 7") while it is partly done
and dated within the last 120 days; at most 3, newest first.

**Changelog.** A `CHANGELOG.md` in [Keep a Changelog](https://keepachangelog.com/)
form gives the project page its `## [Unreleased]` items and the last
released version and date.

## The Docker feed

The feed lets ForgeMap see containers without the Docker socket.
[`scripts/docker-feed.sh`](../scripts/docker-feed.sh) writes
`docker ps -a --format '{{json .}}'` to `$FEED_DIR/docker.json` (default
`/var/lib/forgemap-feed`) by write-and-rename. Run it from the host's cron
every minute:

```cron
* * * * * FEED_DIR=/var/lib/forgemap-feed /usr/local/bin/docker-feed.sh
```

Mount the directory, never the single file (a rename detaches a
single-file bind mount), and point `docker.feed` at the file inside it:

```bash
docker run ... -v /var/lib/forgemap-feed:/feed:ro ...
```

```yaml
docker:
  feed: /feed/docker.json
```

With `docker.host` instead, ForgeMap asks the Engine API directly. A socket
mount gives the container control of Docker even when mounted `:ro`, so
prefer a socket proxy that allows only container listing (for example
`CONTAINERS=1` on a docker-socket-proxy). Mounting the socket itself also
needs its group: `--group-add $(stat -c %g /var/run/docker.sock)`, because
ForgeMap runs as uid 1000.

## The activity feed

`/feed.atom` is an Atom feed of the changes the overview shows: the newest
100 of the last 14 days, newest first. Each entry is one change (a release,
a deploy, a CI run, a change of state, new commits and the rest), titled in
the page's words and linked to its project's page. It shows nothing the
page does not: no private repo the page leaves out, and no note changes
without a notes source. Every page names it in its head, so a feed reader
finds it from the site's address.

| Query | Keeps |
|---|---|
| `?kind=release,ci` | Only these kinds, from `release`, `deploy`, `container`, `ci`, `commits`, `issues`, `probe`, `note` and `tier` |
| `?project=<slug>` | Only one project's changes |

The two can be used together. An unknown kind or project gives an empty
feed. Links use the address the feed was read at, so a proxy in front must
pass the original `Host` header (for nginx, `proxy_set_header Host $host`;
its default sends the upstream's address and every link would point
there). A proxy that ends TLS should also send `X-Forwarded-Proto: https`
so the links start with `https`.

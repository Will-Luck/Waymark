# Changelog

All notable changes to this project are documented in this file.

The format is based on [Keep a Changelog](https://keepachangelog.com/en/1.1.0/),
and this project adheres to [Semantic Versioning](https://semver.org/spec/v2.0.0.html).

## [Unreleased]

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

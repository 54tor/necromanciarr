# Configuration

The configuration lives in the SQLite database. Only the bootstrap, what is needed to open the database, stays outside it: command-line options, environment variables and the `.env` of the configuration folder. Without a database (first run), every value takes the one from cross-seed's `config.js` or its default. `necromanciarr config check` prints the effective configuration, secrets masked, with the origin of each value; the interface's Configuration page, the `config get|set|unset` and `config item set|delete|unset` commands and the `/api/config` routes change it ([04-cli-and-web.md](04-cli-and-web.md)). Each process reads the configuration when it starts: after a change, the response carries `restartRequired: true` and `serve` must be restarted; a following one-off command already sees it.

## Sources and priority

From strongest to weakest: **command-line options**, **environment variables**, **database**, **cross-seed's `config.js`** (if a path is given), **defaults**. A setting forced by an option or a variable can still be changed in the database, with no effect while the option or variable is there: the configuration view marks it `locked`, with origin `env` or `flag`.

| Setting | Option | Variable | Database key |
|---|---|---|---|
| Configuration folder | `--config-dir` | `CONFIG_DIR` | none (bootstrap) |
| Listen address | `--listen` | `NECROMANCIARR_LISTEN` | `listen` |
| Library provider | `--library-api` | `LIBRARY_API` | `libraryApi` |
| SQLite database | `--db-path` | none | none (bootstrap) |
| Read rate | `--read-bytes-per-second` | none | `readBytesPerSecond` |
| cross-seed mode | `--cross-seed-mode` | `CROSS_SEED_MODE` | `crossSeedMode` |
| cross-seed `config.js` | `--cross-seed-config` | `CROSS_SEED_CONFIG` | none (bootstrap) |
| Seed category | none | `NECROMANCIARR_SEED_CATEGORY` | `linkCategory` |
| Seed tags | none | `NECROMANCIARR_SEED_TAGS` (comma-separated) | `seedTags` |
| Display language | `--language` | `NECROMANCIARR_LANGUAGE` | `language` |
| Cap on missing pieces | none | `NECROMANCIARR_PARTIAL_MAX_MISSING_PERCENT` | `partialMaxMissingPercent` |
| Interface token | none | `NECROMANCIARR_AUTH_TOKEN` | none (never in the database) |
| Sealing key | none | `NECROMANCIARR_SECRET_KEY` | none (never in the database) |
| Provider token | none | `LIBRARY_PROVIDER_TOKEN` | `libraryToken` |

An empty variable is ignored. These options apply to every subcommand; an unknown option is a usage error (exit 2).

The configuration folder must exist when it is given. With no option or variable, it is `<user config directory>/necromanciarr` (`~/.config/necromanciarr` on Linux). The Docker image sets `CONFIG_DIR=/data`. The database is `necromanciarr.db` by default; a relative `--db-path` starts from the configuration folder. It stays on a local disk (an `sftp://` location is refused before anything is opened) and outside the library (`doctor`). A relative `config.js` path starts from the current folder.

### Origins (`config check`)

`config check` writes the effective configuration as JSON on stdout, secrets masked. `origins` gives the origin of each setting (`key`) and of each list element (`key` = list, `itemId`): `default`, `crossseed`, `override` (a database value that replaces a default or a cross-seed element), `added` (element added in the database), `env`, `flag`, with `drift: true` for an override whose cross-seed element has changed since. `orphanOverrides` lists the overrides with no element to replace (`List`, `ItemID`). The `sources` key of earlier versions is gone. The language line on stderr gives the origin: `Language: en (source: flag)`.

## `.env`

- Only one `.env` file is read: the one in the configuration folder. If it does not exist, the one in the current folder is read instead; `config check` flags it (`workingDirDotEnv: true`) and `doctor` warns (check `config`). Risk: launched from a folder that holds another tool's `.env`, Necromanciarr would pick up its variables (tokens included); placing its own `.env` in the configuration folder closes that door. The real environment always wins over `.env`.
- Format: `NAME=value`, one per line, `#` comments, `export` prefix tolerated, single or double quotes removed. No multiline value, no interpolation in `.env`.
- The `.env` serves the variables of the table above and those read by cross-seed's `config.js`. No database value is interpolated: `${NAME}` is refused there on write and on load (`${...} references are not supported`, the message names the list, the element and the field, never a value). A configuration that referenced an environment variable takes the value itself, sealed in the database when it is a secret ([Sealed secrets](#sealed-secrets)).

## First-launch bootstrap

`serve` (and only `serve`) completes the `.env` of the configuration folder before loading the configuration:

- Generated one by one, and only when missing (neither in the real environment nor in the `.env`, an empty value such as the one in `.env.example` counting as missing): `NECROMANCIARR_SECRET_KEY` and `NECROMANCIARR_AUTH_TOKEN`. A value that is already present is never modified or replaced; the lines are appended to the end of the file, created with mode `0600` if it does not exist, and tightened to `0600` if it was more open. When only the current folder's `.env` exists (see above), that one is completed. The default folder is created (`0700`) when absent; a folder given by `--config-dir` or `CONFIG_DIR` must exist.
- The new token is shown once, on stderr ("API access token (shown once, also stored in .env): …"), after the line "first start: … generated in the config folder .env". It stays readable in the `.env` and in the container logs (`docker logs`): anyone who can read those logs or that file has it. It is only used to create the operator account, then refused (see [Account and authentication](#account-and-authentication)). To rotate it, delete the `NECROMANCIARR_AUTH_TOKEN` line from the `.env` and restart; or set it yourself (`necromanciarr api-key`).
- Refused when a database already exists but `NECROMANCIARR_SECRET_KEY` is absent: a new key would make the sealed secrets unreadable. Restore the original key; deleting it to get a new one is only safe when the database holds no secret.
- Refused, with an actionable message, when the configuration folder is not writable: make it writable, or define both variables yourself (`necromanciarr api-key`, `api-key --secret`).

Consequence: outside loopback, `serve` no longer exits with an error for lack of `NECROMANCIARR_AUTH_TOKEN` when the folder is writable; it generates one. The other commands never write the `.env`.

The interface then offers the first-launch assistant (see [04](04-cli-and-web.md#first-launch-assistant)).

## Account and authentication

A single operator account (username and password, hashed with argon2id) replaces the `NECROMANCIARR_AUTH_TOKEN` token for web access. The state is read again on every request: switching from one to the other needs no restart.

| State | What the API accepts |
|---|---|
| no account | the `Bearer` token on every protected route, as before; `POST /api/auth/setup` creates the account and requires this token, even on loopback |
| account | the session cookie only; any `Authorization` header, on a protected route as on a public one, gets 401 `bearer_disabled` |

`necromanciarr account reset --yes` returns to the no-account state and reopens the token at once (see [04](04-cli-and-web.md#commands)).

### `publicUrl`

`publicUrl` (`http(s)://host[:port]`) is the address users reach. It fixes:

- the WebAuthn origin and relying-party ID (RP ID), available over https or on `localhost`. Without `publicUrl`, a loopback listener uses `http://localhost:<port>`; any other listener has no WebAuthn.
- the cookies: over https they carry `Secure` and the `__Host-` (session) and `__Secure-` (pre-auth state) prefixes, otherwise they are named `necromanciarr-session` and `necromanciarr-preauth`. The choice follows `publicUrl`, not the request: behind a proxy the application only sees http. Both are `HttpOnly` and `SameSite=Strict`; the session applies to the whole site, the pre-auth state to `/api/auth` only.
- the `Origin` check on writes: when set, `publicUrl` is the only admitted origin (a proxy may rewrite `Host`).
- the `Host` check: on a loopback listener, the `publicUrl` host is added to the admitted hosts; outside loopback there is no `Host` check.

A host that is an IP address (`https://203.0.113.7`) or ends with a dot cannot be a WebAuthn identifier: security keys and passkeys are unavailable and `doctor` warns (`auth_public_url_unusable`).

### First launch

`serve` generates the token (see [Bootstrap](#first-launch-bootstrap)). With no account, the interface asks for this token, then a username (1 to 64 characters) and a password, and opens the session. The token is only used on that screen: once the account exists it is refused (`bearer_disabled`) until an `account reset`. The interface then offers, without forcing it, to add a second factor.

### Factors

| Factor | Role | Notes |
|---|---|---|
| password | first factor | at least 12 characters, at most 128 bytes |
| security key (`second_factor`) | second factor | never signs in alone |
| passkey (`passkey`) | sign-in without a password | requires user verification (PIN or biometrics) and a resident key; opens the session directly |
| TOTP | second factor | requires `NECROMANCIARR_SECRET_KEY`, which seals the secret (without it: 503 `totp_unavailable`); 6 digits, 30 s, one step of tolerance each side; a code works once per step |

As soon as a security key or a TOTP exists, the password alone no longer opens a session: the sign-in answers `second_factor_required` and the session is only issued after the key or the code. Adding or removing a factor, or changing the password, asks for the current password. Cancelling the browser prompt keeps the choice of factor; a refusal by the server (wrong code, key refused) goes back to the password, because the step state is consumed on the first try. Likewise, a wrong confirmation code when adding a TOTP, or a failed key ceremony, means starting the addition again with the password.

### Limiter and password

The limiter counts per account, with no permanent lock-out: 1 s wait after the first failure, doubled on each failure, capped at 60 s. Password, TOTP, keys, passkeys and password entries to change a setting all share it. An attempt made too early gets 429 `throttled` with `Retry-After` in seconds. A correct password that still waits for a second factor does not reset the counter; only a successful sign-in does. An unknown username and a wrong password give the same answer (401 `invalid_credentials`) at the same computing cost. A password over 128 bytes is refused before any computation and is not counted. `account reset` does not clear a running delay (60 s at most).

### Sessions

A session lasts 14 days at most and expires after 24 h without a request. The database only keeps a fingerprint of the token. "Sign out everywhere" revokes all of them; changing the password revokes the other sessions and keeps the current one; `account reset` deletes them all.

### In-memory state and accepted limits

Sign-in tickets and pre-auth states (5 min), and key or TOTP enrolment states (10 min) live in memory and work once; the web server's tables are bounded to 1024 entries, the oldest being evicted. A restart loses them: sign in again or restart the addition. Sessions are in the database and survive.

Accepted v1 limits:

- A single process is assumed: these states are not shared.
- `POST /api/auth/passkey/login/begin` is public and unthrottled; its flood is bounded by its own table (which never evicts a pending second factor) and by the throttled finishes.
- About 1024 passkey sign-in begins followed by invalid finishes fill the set of consumed nonces for about 6 min; the real user then gets a failure counted by the limiter.

### WebAuthn unavailable

If `publicUrl` becomes http (outside `localhost`) or an IP address, keys no longer work: passkey sign-in answers 503 `webauthn_unavailable`, and so does password sign-in when keys are the only second factor. With a TOTP registered, sign-in continues with the code. Otherwise put `publicUrl` back on the https name, or run `necromanciarr account reset --yes` and recreate the account.

## Database values

Settings live in the `settings` table (one JSON value per key, replaced as a whole); the `radarr`, `sonarr`, `torrentClients`, `linkDirs`, `torznab`, `trackers` and `fsProviders` lists live in `list_overrides`, element by element ([Lists and per-element override](#lists-and-per-element-override)). An unknown key, an unreadable value or an invalid element blocks loading: the message names the key or the element, never the value, and `doctor` fails its `config` check. The old `rclone` section is an unknown key: declare it in `fsProviders`.

| Key | Type | Default | Validation |
|---|---|---|---|
| `listen` | `host:port` | `127.0.0.1:8787` | outside loopback, `serve` requires `NECROMANCIARR_AUTH_TOKEN`; the other commands do not |
| `readBytesPerSecond` | integer (bytes/s) | `20971520` (20 MiB/s) | strictly positive |
| `tieBreakReadBytes` | integer (bytes) | `53687091200` (50 GiB) | strictly positive. Maximum bytes read, per candidate, to tell same-size local files apart; coverage is not affected |
| `libraryApi` | URL | empty | `http(s)`, no fragment; sealed in the database. Empty: Match and Verify refuse (`not_configured`) |
| `libraryToken` | text | empty | Bearer token sent to the library provider; sealed in the database. `LIBRARY_PROVIDER_TOKEN` wins when it is set and not empty |
| `libraryRoots` | list of paths | empty | as seen by this process; absolute paths without a `..` segment (refused at load time). An `sftp://<provider>/<absolute path>` location targets a declared `fsProviders` entry of type `sftp` (example: `sftp://seedbox/srv/media`); an `rclone` provider is refused. The server is reached only at first use: when it is unreachable, startup, local operations and `doctor` still work; Scan then leaves its roots as they are (`remote_unavailable`) and the next stage tries once more |
| `pathMappings` | list of `{from, to}` | empty | `from`: this process's view (a local path, or an `sftp://<provider>/<absolute path>` location of a declared provider); `to`: the view of Radarr, Sonarr and the torrent client, never an `sftp://` location |
| `radarr`, `sonarr` | list of `{url, apiKey, user, password, proxyKey}` objects | empty | `url` as `http(s)://host:port[/path]`, in clear, without any secret or query parameter; `apiKey` required, `user` and `password` optional (basic-authentication proxy), `proxyKey` for a proxy key in the path (`/proxy/{proxyKey}`). cross-seed gives them as a `http(s)://[user:password@]host:port[/path]?apikey=KEY` string, split the same way; in that string only, `apikey` is the only parameter allowed |
| `torrentClients` | list of `{type, readOnly, url, user, password, proxyKey}` objects | empty | `type` is `qbittorrent`, `url` as `http(s)://host:port[/path]` in clear, with no query parameter; `proxyKey` as for `radarr`. cross-seed gives them as a `qbittorrent[:readonly]:http(s)://[user:password@]host:port[/path]` string. Only the first client receives torrents (warning if there are several) |
| `linkDirs` | list of `{path}` objects | empty | absolute paths without `..`; never overlaps a `libraryRoots` root of the same volume |
| `linkType` | `hardlink`, `symlink`, `reflink` | `hardlink` | a single kind, no fallback; a failure states the cause and the setting to change |
| `skipRecheck` | boolean | `true` | see [03-seed-safety.md](03-seed-safety.md) |
| `linkCategory` | string | empty | qBittorrent category of the added torrents; `NECROMANCIARR_SEED_CATEGORY` wins (spaces removed) |
| `seedTags` | list of strings | `["necromanciarr"]` | tags set together with the category on each added torrent. The list **replaces** the default: without `necromanciarr`, that tag is no longer set (qui or cross-seed filters that target it no longer see these torrents). Spaces removed, duplicates ignored, empty list = default; an empty tag, one containing a comma or longer than 64 characters, and more than 20 tags are refused |
| `language` | `fr`, `en` | `fr` | language of the displayed texts; case and spaces ignored, any other value (`fr_FR` included) is a usage error that quotes the value (20 characters at most) and the accepted values. Applied at this stage: text output, help and errors of the CLI, web API errors and descriptions of the action registry. To come: diagnostics (`doctor`), warnings and the server log. `config check` writes the effective language and its source on stderr |
| `partialMaxMissingPercent` | number from 0 to 100 | `100` (no cap) | maximum share of missing pieces of a `partial_annex` or `partial_pack`; above it, no proposal is created (reason `missing_pieces_above_cap`, see [01-pipeline.md](01-pipeline.md)). `0` keeps only complete torrents |
| `crossSeedUrl` | URL | empty | `http(s)://host:2468`, no credentials, query or fragment; clear in the database; the key goes in `crossSeedApiKey` |
| `crossSeedApiKey` | string | empty | API key of the cross-seed daemon; sealed in the database, never returned; needs `crossSeedUrl` or an imported `host`/`port` |
| `publicUrl` | URL | empty | `http(s)://host[:port]` that users reach, without path, query, fragment or credentials; clear in the database. Lower-cased, trailing slash and default port dropped. Pins the WebAuthn origin and RP ID (available over https, or on `localhost`) and the `Secure` attribute of the session cookie; without it, a loopback listen uses `http://localhost:<port>` |
| `crossSeedMode` | `auto`, `delegate`, `native`, `off` | `auto` | case and spaces ignored |
| `dedupe` | object | see below | |
| `fsProviders` | list of objects | empty | see below |
| `torznab` | list of objects | empty | see "Indexers" below |
| `prowlarr` | URL | empty | see "Indexers" below |
| `sourceOrder` | list of strings | `provider`, then the `torznab` entries | see "Indexers" below |
| `trackers` | list of `{key, announce, aliases}` objects | empty | see "Tracker announces" below |

### Paths and mappings

The paths of the library, link directories and torrent roots are those seen by **this process**. `pathMappings` translates to the other views: the torrent client receives the torrent folder translated from `from` to `to`; the paths returned by Radarr and Sonarr are translated from `to` to `from`. The longest prefix wins, matching happens on a segment boundary (`/data/tv` does not cover `/data/tvshows`), on a tie the first rule wins. A side that starts with a drive letter or `\\` is a Windows path: `\` or `/` separators accepted, case ignored. With no applicable rule, the service is assumed to see the same path, except for a remote link folder: Seed then refuses (`client_path_unmapped`, see [05-docker-and-volumes.md](05-docker-and-volumes.md)).

### Link directories

- No `linkDirs[i]` equals a library root, contains it or is contained in it: refused at load time (lexical), then at the Link stage on resolved paths (symlinks followed).
- For each torrent, Link picks the **first** `linkDirs` folder located on the same filesystem as the first source file. Hardlink and reflink require that same filesystem. With no matching folder, only a symlink is possible, and only if `linkType` is `symlink`: otherwise the proposal becomes `failed` (`no_link_dir_on_same_device`).
- The torrent folder is named `<root name> [<full infohash>]` under the link directory, with `0755` permissions so that the client, often under another account, can read it. A name longer than 255 bytes shortens the root name (without cutting a character), never the infohash. Seed and Force always use the folder recorded for the proposal (`proposal_steps.link_root`), including a `<root name> [<8 characters>]` folder created by an earlier version. After damaged links, Seed erases the step but keeps this folder; Link reuses it only if it sits directly under the link directory chosen for this run, otherwise it creates a new folder named with the full infohash.
- `linkType` is the only kind setting for Link: if the folder does not accept it (real probe), the proposal fails (`link_kind_refused`) instead of changing kind.
- A link kind failure (`cross_device`, `no_link_dir_on_same_device`, `link_kind_refused`) leaves on the proposal a warning that names the cause and the expected action, for example "hardlink impossible between two mounts: put linkDirs under the same mount point as the library, or choose linkType symlink/reflink". After the fix, re-accept the proposal then rerun Link.

### Torrent clients, qui, read-only mode

- `readonly:` forbids any write: Seed stops (`ErrClientReadOnly`), deduplication cannot request a recheck (warning `recheck_failed`). Reading (list, doctor) remains allowed.
- **qui** (autobrr) declares itself as a qBittorrent behind its proxy: `qbittorrent:http://host:7476/proxy/<key>`, without credentials. The key after `/proxy/` goes into `proxyKey`, sealed; the `url` keeps `/proxy/{proxyKey}` in clear and the key is masked in all outputs.
- The client is called without an environment proxy and without redirection to another host; response bodies are capped.
- Only qBittorrent is supported. A `deluge`, `transmission` or `rtorrent` client imported from cross-seed is discarded and reported by rank and type.

## Lists and per-element override

cross-seed provides `radarr`, `sonarr`, `torrentClients` and `linkDirs`; the database keeps only what the operator replaces, hides or adds, never a copy of the cross-seed values. Each element has an identifier (`itemId`):

| List | Identifier |
|---|---|
| `radarr`, `sonarr`, `torznab`, `torrentClients` | `scheme://host[:port]` of `url`, in lower case, without the scheme's default port (80 for http, 443 for https), with no path or credentials |
| `linkDirs` | `path` |
| `trackers` | `key` |
| `fsProviders` | `name` |

- Two cross-seed elements with the same identifier get `#2`, `#3`… in their order (`http://radarr:7878#2`).
- For a new element of these four lists, an identifier of any other form (path, credentials, query) is refused; the interface derives it from `url` until it is edited. A row already stored keeps its own.
- Effective list: the cross-seed elements in their order, each replaced by its override when it has one or removed when it carries a deletion marker, then the additions in insertion order.
- An override replaces the whole element. It follows the identifier, not the rank: it keeps the identifier of the targeted element even when it changes its `url`, and stays attached to that element if cross-seed reorders its list.
- A fingerprint of the cross-seed element is kept with the override: if the element changes later, the override still applies and carries `drift: true`.
- An override or a marker whose element has left cross-seed is orphaned: it is not applied, `config check` (`orphanOverrides`) and the Configuration page report it, nothing is deleted unless asked. An unreadable override leaves the cross-seed element in place and becomes orphaned.
- `torznab`, `trackers` and `fsProviders` come from the database only: each element there is an addition, under the identifier given on write.

## Sealed secrets

Secret fields are sealed in the database (AES-256-GCM, key `NECROMANCIARR_SECRET_KEY`, bound to their list, element and field: a sealed value copied elsewhere does not open).

| List or setting | Sealed fields |
|---|---|
| `radarr`, `sonarr` | `apiKey`, `password`, `proxyKey` |
| `torrentClients` | `password`, `proxyKey` |
| `trackers` | `announce` |
| `torznab` | `apiKey` |
| `fsProviders` | `rc`, `password`, `identityPassphrase` |
| settings | `prowlarr`, `crossSeedApiKey`, `libraryApi`, `libraryToken`; `crossSeedUrl` stays in clear |

- The `url` of `radarr`, `sonarr`, `torrentClients` and `torznab` stays in clear, without any secret. It is refused on write and on load (`<list>.url must not carry a secret nor a query`, with a message that never repeats the URL) if it is unreadable, carries credentials (`user:password@`) or any query, or if a `proxy` segment (case ignored) is not followed by `{proxyKey}`, or if a decoded segment contains `/` (`%2F`). `{proxyKey}` is allowed only once, right after that segment. A `proxyKey` without a `{proxyKey}` segment is refused (`proxyKey is set but url has no {proxyKey} segment`); `""` is tolerated. On load, `{proxyKey}` receives the key escaped for a path (`@` included), and an indexer's `apiKey` joins the URL as `?apikey=`.
- A row from an earlier format, with a sealed value in a clear field, blocks loading (`sealed value in a clear field: item from an earlier format; remove the old rows first (config item unset), then add them again`). `config item unset` removes it even while other rows of that format remain: removing a row from a database that already fails to load is always allowed. Rewriting such a row stays refused while another one remains: remove all the old rows first, then add them again.
- Without `NECROMANCIARR_SECRET_KEY`, writing a secret is refused (`secret_key_missing`, HTTP 409) and nothing is written. Generate the key with `necromanciarr api-key --secret`.
- On load, a sealed secret that does not open (missing or changed key) blocks startup: `sealed config secrets cannot be read: NECROMANCIARR_SECRET_KEY is missing or changed`. Changing the key means entering every secret again.
- The configuration view (`config get`, `GET /api/config`, Configuration page) never returns a secret: the field is `null`, with `secret: true`, and `set` tells whether a value exists. Rewriting an element without one of its secrets keeps the stored value (or the cross-seed one), unless its destination changes (`url` or `proxyKey`, or `address` of an `fsProviders` entry): the write is then refused (`re-enter the secrets when url changes`, or `… when proxyKey changes`), so a secret never leaves for another host. Removing a proxy means writing a `url` without `{proxyKey}` and entering the other secrets again: the stored `proxyKey` goes away with the element.

## Importing cross-seed's `config.js`

With `CROSS_SEED_CONFIG` or `--cross-seed-config`, the file is evaluated in a JavaScript sandbox with no input/output: no `require`, no `import`, no `eval`, no `Function`; only `process.env.NAME` exists, read from the real environment then the `.env`. This tool's variables (`NECROMANCIARR_*`, `LIBRARY_PROVIDER_*`) are always absent there (`undefined`, `in` false): a `config.js` that reads them gets nothing, `config check` names them under `crossSeedImport.blockedEnvironment` and `doctor` (check `cross-seed`) warns, never showing their value. Limits: ordinary file of at most 256 KiB, 2 seconds of evaluation, 256 entries per list. A relative path starts from the current folder. The file is read again at every start: its values are never copied into the database.

| Imported key | Role | Merge rule |
|---|---|---|
| `radarr`, `sonarr`, `torrentClients`, `linkDirs` | lists | element by element ([Lists and per-element override](#lists-and-per-element-override)) |
| `linkType`, `linkCategory` | strings | the database if non-empty, otherwise the imported value |
| `skipRecheck` | boolean | the database if present, otherwise the imported value |
| `host`, `port` | cross-seed daemon (`crossSeedUrl`) | `crossSeedUrl` in the database wins; otherwise `http://host:port` (empty `host`, `0.0.0.0` or `::` becomes `localhost`, port 2468 by default) |
| `apiKey` | cross-seed daemon (`crossSeedApiKey`) | `crossSeedApiKey` in the database wins; the address and the key are replaced separately |

The other keys (indexer search rules) are ignored; `config check` lists their names (`ignoredKeys`) but never their value; the origin of each value is in `origins`. In Docker, the `localhost` in `config.js` designates the cross-seed container: store `crossSeedUrl` (and `crossSeedApiKey` if the key differs) in the database. An old stored `crossSeed` setting blocks loading: remove it with `config unset crossSeed`, then set the two new ones.

### cross-seed modes

| `crossSeedMode` | Effective behaviour |
|---|---|
| `auto` | `delegate` if the daemon is configured and reachable (TCP connection, 2 s), otherwise `native` (warning if the daemon is configured but unreachable) |
| `delegate` | notifies the daemon after each seed; without a reachable daemon, search is disabled with a warning, never a fallback to `native` |
| `native` | Necromanciarr creates `cross_seed` proposals for the other verified torrents of a group; the daemon is ignored |
| `off` | no matching search: one torrent per group |

In `delegate`, the notification is `POST /api/webhook` with the `X-Api-Key` header, sent only when the torrent has been resumed at 100%. Its failure adds a warning to the proposal, never a seed failure. `status` returns the effective behaviour (`delegate`, `native`, `disabled`). The daemon's reachability is probed again at the start of each stage, each acceptance (`accept`, `accept-group`) and each `status`, with a result kept for 30 seconds: a long-running `serve` follows the daemon stopping and coming back without restarting. The probe has its own 2-second timeout, independent of the request that triggers it; if that request is cancelled in the meantime, the result is not kept. A `cross_seed` proposal can only be accepted if the effective mode at that moment is `native` (`a cross_seed proposal can only be accepted in native cross-seed mode`, code `refused`).

## Deduplication (`dedupe`)

Replaces, on the torrent side, a copy of a library file with a link to it. Always proposed, never automatic; the library file is never opened for writing, moved or deleted.

| Key | Type | Default | Role |
|---|---|---|---|
| `torrentRoots` | list of paths | empty | absolute paths without `..`; folders scanned. Empty: feature inactive (`not_configured`). Never overlaps a library root |
| `kinds` | list among `symlink`, `hardlink`, `reflink` | `["symlink"]` | kinds tried in this order, depending on what the volume accepts (real probe) |
| `relativeSymlinks` | boolean | `true` | relative symlink; if impossible (distinct Windows volumes), error, never a fallback to an absolute path |
| `minFileAge` | Go duration ≥ 0 | `10m` | files modified more recently are ignored (being written) |
| `recheckAfter` | boolean | `true` | asks the client to recheck every torrent that contains the file (list read page by page, see [06](06-limits-and-troubleshooting.md)): all are paused, rechecked, then only a complete torrent **that was running before** is resumed; an incomplete torrent, or one the user had left paused or stopped, stays paused |
| `verify` | `auto`, `full`, `sampled`, `hash` | `auto` | identity proof |

Identity proof: `full` compares every byte; `sampled` compares the start, the end and 8 blocks of 1 MiB (a difference elsewhere goes unnoticed); `hash` requires the storage fingerprint and fails without it; `auto` uses the storage fingerprint when both files have one, otherwise reads in full, never by sampling unless asked. The plan announces `bytes_to_read` before acceptance. In all cases, `plan` only keeps files of the same size and same start and end fingerprint (1 MiB each), and `apply` redoes this check, verifies that size and date have not moved since the plan, creates the link under a temporary name then renames it over. `dedupe plan` first picks up the temporary entries of an interrupted application.

With no torrent client configured (or with a `readonly:` client), or with `recheckAfter: false`, a `symlink` or `hardlink` proposal carries the warning `no_client_recheck` (CLI: line `warning: no_client_recheck` under the proposal; web: "Warning"): the torrent will point to the library file without a recheck, and a running incomplete torrent could write to it. The proposal remains acceptable; a `reflink` is not affected (copy on write).

A file recorded in `links` as `copy` or `reflink` (copy placed by Force, or reflink from Link) is never linked back to the library: `dedupe plan` lists it with `actionable: false` and `reason: isolated_copy`, and `dedupe apply` refuses it, so as not to undo the isolation of a `partial_annex`.

## Link repair

`repair scan` examines the recorded links, then the unknown links under `dedupe.torrentRoots` and under the folder given by `--dir` (refused if it overlaps the library). For a broken link it looks for the new target by inode (local disk only), then size and date, then start-end fingerprint (at most 5 reads per link), then the pieces of the client's torrent. Several candidates: `ambiguous`, nothing is chosen. Nothing found: `lost`. Only a `repairable` link can be accepted. `repair apply` only replaces a link under `linkDirs`, `dedupe.torrentRoots` or the scan folder, never under the library.

With a torrent client configured, each replaced link makes the torrent that contains it be rechecked, under the same guard as dedupe (client view through `pathMappings`, pause, recheck, resume only at 100%): an incomplete torrent stays paused with the warning `paused_incomplete_after_recheck`, a torrent left paused by the user, or in the `error` or `missingFiles` state, is never resumed, an impossible recheck (no torrent covers the link) gives the warning `recheck_failed`, never a failure. A client declared `readonly:` counts as "no client". Without a client, only the `inode` and `torrent-pieces` methods, which prove the content, are applicable: a repair found by `size-mtime` or `fingerprint` is listed with `actionable: false` and `reason: needs_client_recheck` (CLI: line `not actionable: needs_client_recheck`; web: badge "Not applicable without a torrent client: the method waits for its recheck"). For these two methods, the torrents covering the link are listed before the rename: a failed read (unreachable client, page larger than 8 MiB, list that keeps moving, more than 200,000 torrents) refuses the application (`the torrents covering this link cannot be listed, repair refused`), nothing is replaced.

## Filesystem providers (`fsProviders`)

Two types: `rclone` gives the content fingerprint of a mounted file without rereading it; `sftp` exposes remote storage over SFTP, named by `sftp://<name>/<absolute path>` locations in `libraryRoots`, `linkDirs`, `dedupe.torrentRoots` and `pathMappings[].from`. Each entry is read according to its type: a key of another type is refused and named.

### Type `rclone`

`rclone hashsum`, never `--download`.

| Key | Default | Rule |
|---|---|---|
| `type` | required | `rclone` |
| `name` | required | `[a-z0-9-]+`, unique |
| `mounts` | empty | `"/local/path=remote:sub/path"`, a single `=`, absolute local path without `..`, unique across providers. Nesting is allowed: the longest prefix wins |
| `rc` | empty | `http(s)` URL of the `--rc` API of the rclone process that carries the mount; `user:password@` removed from the URL and sent as basic authentication; redirections refused. Takes priority over `binary` |
| `binary` | `rclone` from the PATH | executable |
| `timeout` | `60s` | strictly positive Go duration |

The `rc` methods called form a closed, read-only list. Start rclone with `--rc --rc-addr :5572 --rc-user <utilisateur> --rc-pass <motdepasse>` and never expose `rc` outside a private network: whoever reaches it drives rclone. Without `rc` or binary, no fingerprint is available and the comparison rereads the files.

`dedupe plan` and `dedupe apply` ask the storage for the algorithms its mounts provide: the first time, each mount is probed on a test file (MD5, SHA-1, SHA-256) and the result is kept in the database (table `mount_capabilities`, one row per mount); afterwards only the database is read. A failed probe is not kept. The cache does not refresh by itself: after a backend or `rc` change, delete the database or the mount's row. The `rclone` check of `doctor` always probes again, without writing this cache.

### Type `sftp`

`seedbox.json`, read from standard input so the passphrase does not stay in the shell history:

```json
{
  "type": "sftp",
  "name": "seedbox",
  "address": "seedbox.example.com:22",
  "user": "media",
  "identityFile": "/data/ssh/id_ed25519",
  "identityPassphrase": "dummy-passphrase",
  "knownHosts": "/data/ssh/known_hosts"
}
```

```
necromanciarr config item set fsProviders seedbox --element-stdin < seedbox.json
necromanciarr config set libraryRoots '["sftp://seedbox/srv/media"]'
necromanciarr config item set linkDirs sftp://seedbox/srv/links '{"path": "sftp://seedbox/srv/links"}'
```

| Key | Default | Rule |
|---|---|---|
| `type` | required | `sftp` |
| `name` | required | `[a-z0-9-]+`, unique; it is the `<name>` of the `sftp://<name>/…` locations |
| `address` | required | `host` or `host:port` (host name or IP address), port `22` by default; IPv6 between brackets (`[::1]` or `[::1]:22`); no URL (`sftp://…`), control character, stray bracket or `known_hosts` pattern character (`,` `*` `?` `!`); two providers of the same address (host without case or trailing dot, explicit port) share their library roots; a DNS name and its IP address are not matched; a loopback volume and a local path can name the same disk: not detected, `doctor` warning (`volume_alias_risk`) |
| `user` | required | SSH account; an account with no write access to the library is recommended |
| `knownHosts` | required | absolute local path of an OpenSSH `known_hosts` file; the host key is always checked, no option skips it. Whether the file exists is a `doctor` matter |
| `identityFile` | empty | absolute local path of a private key (ed25519, ecdsa or rsa ≥ 3072); a shorter RSA key is refused (`identity_key_too_weak`). Agent keys are not filtered |
| `identityPassphrase` | empty | passphrase of `identityFile`, sealed in the database; needs `identityFile` |
| `agent` | `false` | SSH agent (`SSH_AUTH_SOCK`), read at connection time only |
| `password` | empty | sealed in the database; the SSH `password` method only (not `keyboard-interactive`); the least recommended method |
| `readBytesPerSecond` | global rate | bytes per second; `0` or absent keeps the global `readBytesPerSecond`, both limits apply |
| `maxConcurrentRequests` | `16` | SFTP requests in flight per file, integer from 1 to 256 |
| `timeout` | `30s` | timeout of one SFTP operation, strictly positive Go duration |
| `keepalive` | `15s` | interval of the OpenSSH keepalive requests (three unanswered: dead session), strictly positive Go duration |
| `exec` | `auto` | `auto`, `off` or `required`: read-only remote commands (`stat`, `sha1sum`, `md5sum`) |
| `execHashTimeout` | `30m` | timeout of one batch of remote fingerprints, strictly positive Go duration |

- At least one authentication method: `identityFile`, `agent` or `password`. They are tried in the order agent, key, password. The `~/.ssh` folder is never read implicitly.
- `password` and `identityPassphrase` are entered as they are and sealed on write; `config check` and every output mask them; `identityFile`, `knownHosts`, `address` and `user` stay visible.
- `mounts`, `rc` and `binary` do not apply to the `sftp` type: refused.
- The SQLite database (`--db-path`) stays on a local disk.
- Windows SFTP servers (`/C:/…` paths): not supported.
- Connection: `timeout` also bounds the opening (TCP, SSH, SFTP). An operation that exceeds `timeout`, or three unanswered keepalives, closes the whole connection: the volume becomes `broken` and the stage stops (`remote_unavailable`). It reconnects on the next operation, once per stage.
- Each agent key counts toward the server's `MaxAuthTries`: an agent holding many keys can exhaust the attempts before the `identityFile` key and the password. Set `agent: false` or load fewer keys. An agent key identical to the `identityFile` one is offered only once.
- `knownHosts` missing, unreadable or without a valid entry: `known_hosts_unreadable` refusal.
- `agent` without `SSH_AUTH_SOCK` (or an unreachable socket, or an agent that does not answer within `timeout`): the method is skipped with the `agent_unavailable` warning, which names the variable, never the socket path; an `identityFile` readable by group or others: `identity_file_permissions` warning, not a refusal.

#### Rate and limits

- Read only: the remote library is read through `SSH_FXF_READ`-only opens; no write request is ever sent there.
- Rate: each read waits, before it is sent, on the provider limiter (the provider's `readBytesPerSecond`) then on the global limiter (the global `readBytesPerSecond`); the stricter of the two wins. Bytes are counted when requested, in 32 KiB slices. A read split into `maxConcurrentRequests` requests in flight never carries more than what was counted.
- Directory listings: a name that is empty, `.`, `..`, contains `/` or NUL, or is longer than 4096 bytes is dropped, as is an entry with an impossible size (negative or beyond 2^62 bytes); a duplicate name keeps only its first entry. Beyond one million entries, the listing is refused. The received listing is held whole in memory before this check: an endless listing is stopped only by `timeout`, and can take about throughput × `timeout` × 10 bytes of memory (a decoded entry weighs about ten times its size on the network). Names with non-UTF-8 bytes stay readable.
- A read that stops before the file's size (a file still growing, a server cutting the read, a dropping connection) is reported as unavailable (`remote_unavailable`), never as end of file: the file is not judged different.
- A link target longer than 4096 bytes, an impossible file size or an inconsistent `statvfs` answer is refused, never truncated.
- Free space and volume identity: read through the OpenSSH `statvfs` extension. Without it, free space is unknown (no full copy is attempted) and so is the volume identity. The volume identity comes from the server's `statvfs`: a lying server only skews the choices of the scan and of links (same volume or not), never a write into the library.

#### Remote writes

- Links are placed in a link folder on the same server (for example `sftp://seedbox/srv/links`), outside every library root; every write goes through the confined write root of that folder.
- OpenSSH extensions used: `hardlink` for a hard link, `posix-rename` for an atomic replace. Without `hardlink`, a hard link is refused as an unsupported kind. Without `posix-rename`, nothing is replaced (`no_atomic_replace`): never a plain rename, never a delete then create. A symbolic link needs neither: its target is sent as given and the server reads it from the link's folder. On each session, a probe creates then removes a test link in the link folder to check the argument order; a server that reads them in the reverse order (link placed where the target should be) has its symbolic links refused.
- Force (isolation by copy) is refused on an SFTP volume (`server_copy_unavailable`): the client offers no server-side copy (`copy-data`), and a stream copy would carry the whole media across the network ([03-seed-safety.md](03-seed-safety.md)).
- Probes: before relying on a function, the volume tries it in the first link folder, never in a library: symbolic link (argument order), hard link, atomic replace (`posix-rename`), `statvfs`. A function is kept only if the server advertises the extension **and** the try succeeds; otherwise its reason is `extension_missing` (not advertised, no request sent) or `probe_failed` (advertised, the try failed). Server-side copy is always `server_copy_unsupported_by_client`. Probe files are named `.necromanciarr-probe-` followed by 32 hexadecimal characters, are removed in every case, and the leftovers of an interrupted probe are removed by the next probe (only these exact names, never a folder, never recursively). The result holds for the session; the one stored in the database (`volume_capabilities`) is only a display hint, the next session probes again. Before the first probe, none of these functions is advertised (`not_probed`).
- Confinement: a written path is relative to the link folder, with no `..`, no empty segment and no NUL. Each parent folder is checked (a symbolic link is refused), and the server's resolution of it (`realpath`) must stay its own path, before and after the write, the removal or the replace; an entry created under a diverted parent is reported (`ErrUnconfined`), never removed: removing it would follow the same diverted parent. SFTP has no folder-relative open: between the last check and the write, the removal or the replace, an actor who can already write in the remote link folder can still divert them. This risk is accepted; the recommended defence is an SFTP account that writes only in the link folder and only reads the library ([05-docker-and-volumes.md](05-docker-and-volumes.md)).
- A folder created writable by group or others (the account's umask) gives the warning `link_dir_world_writable`, never a failure; the requested permissions are never set by path.
- A lost session invalidates the write root: the operation returns `remote_unavailable`, and the next stage reopens and re-checks the root.

#### Read-only remote execution

SFTP gives no inode, no link count and no hash computed by the server. When `exec` allows it, the volume runs, on the same SSH connection but in separate sessions, a closed list of read-only commands: `stat` (device, inode, links, size, date) and `sha1sum`/`md5sum` (full hash computed by the host). No write, no link and no rename ever goes through exec: writes stay SFTP, inside the confined root.

- Closed list: the lines sent are constants, built once. No file name is ever in them: paths travel on standard input, NUL-separated, read by `xargs -0`, after `--`. Outputs are NUL-terminated records (`--printf=…\0`, `-z`): a name holding a newline shifts nothing.
- Possible lines (`<TAB>` is a tab character):

```
printf %s 'a'\''b\' '!x'
'stat' '--version'
'xargs' '--version'
'sha1sum' '--version'
'md5sum' '--version'
'nice' '--version'
'ionice' '--version'
'xargs' '-0' '-r' 'stat' '--printf=%d<TAB>%i<TAB>%h<TAB>%s<TAB>%Y\0' '--'
'xargs' '-0' '-r' 'sha1sum' '-z' '--'
'xargs' '-0' '-r' 'md5sum' '-z' '--'
'nice' '-n' '10' 'ionice' '-c3' 'xargs' '-0' '-r' 'sha1sum' '-z' '--'
'nice' '-n' '10' 'ionice' '-c3' 'xargs' '-0' '-r' 'md5sum' '-z' '--'
```

- POSIX login shell required: the server has each line read by the account's login shell (`$SHELL -c`). The first line of every session is a constant probe: its output must be exactly `a'b\!x`. Under fish or csh it differs: all remote execution is disabled for the session (`login_shell_not_posix`), constant lines included, and the volume carries on SFTP-only. A probe line that does not finish (timeout, oversized output) gives `exec_probe_failed`: SFTP-only for the stage, probe run again on the next stage.
- Probe: next, `stat`, `sha1sum` and `md5sum` must be GNU coreutils, `xargs` GNU findutils; `nice` and `ionice`, when present, add the `nice -n 10 ionice -c3` prefix to hashes, so the host's disk is not crushed. Without GNU `xargs`, each file goes in its own `stat --printf=… -- '/…'` command: the absolute path is then the only external argument, in POSIX single quotes. The verdict holds for the session.
- Admitted paths: absolute, canonical, without NUL, at most 4096 bytes, under a library root, a link folder or a torrent root (`dedupe.torrentRoots`) of the same volume; otherwise refused before anything is sent.
- Bounds: 64 files per command (a longer list is split), one command at a time per volume; standard output capped at 64 KiB for `stat` and at 64 × (path length + 128) bytes for hashes; standard error read up to 4 KiB and never echoed (it may quote paths): only the exit status and a typed reason come back. Timeout of 30 s per `stat` batch, `execHashTimeout` per hash batch; past it, the session gets `KILL` and is then closed (an OpenSSH server older than 7.9 ignores the signal: closing the channel remains).
- Checks: every `stat` record has exactly five numeric fields, and its size must equal the one SFTP gives for the same path; every hash (lowercase hexadecimal) must be followed by the path sent, byte for byte, in the order sent. A missing, extra or malformed record rejects the whole batch: no partial result.
- `exec: auto`: probe on first use; exec refused by the server (`exec_denied`) or a failed probe: SFTP-only, no error. `exec: off`: never any exec. `exec: required`: exec being unavailable is an error.
- Server-side hardening (recommended, not required): a dedicated key in `authorized_keys`, with `restrict` and a forced guard command (`restrict,command="<guard>" ssh-ed25519 …`). The guard compares `SSH_ORIGINAL_COMMAND` byte for byte with the lines above, runs a recognised line through `/bin/sh -c` and refuses everything else; it lets the SFTP subsystem through. The one-file-per-command lines cannot pass such a guard: a guarded host needs GNU `xargs`. With an account that can only read the library, exec cannot write there even if a call escaped the code.

#### Getting the `known_hosts` line

The tool never trusts an unknown host key on its own: only the `sftp trust <name> --yes` command, run by the operator, adds a line to `knownHosts` ([04-cli-and-web.md](04-cli-and-web.md)). To fill the file safely:

1. On the server, through an access you already trust (console, existing session), print the fingerprint: `ssh-keygen -lf /etc/ssh/ssh_host_ed25519_key.pub`.
2. With the tool: `necromanciarr sftp trust seedbox` prints the presented fingerprint without writing anything; if it equals the one from step 1, run it again with `--yes`. In Docker, run the command inside the container, with the `knownHosts` folder mounted writable for the time of this addition ([05-docker-and-volumes.md](05-docker-and-volumes.md)).
3. Or without the tool, from its machine: `ssh-keyscan -H -p 22 seedbox.example.com >> /data/ssh/known_hosts`, then `ssh-keygen -lf /data/ssh/known_hosts`. Compare with the fingerprint from step 1; if they differ, remove the line: the connection may be intercepted.

Hashed entries (`-H`) are supported. Only the key types present for the host are asked of the server first: a server that also has a key of another type is not wrongly refused. The file is reread on every connection.

## Indexers (`torznab`, `prowlarr`, `sourceOrder`)

A Torznab indexer is a fallback source of `.torrent` files, after the library provider. Each element of `torznab` is an object; `tracker-a.json`:

```json
{
  "url": "https://prowlarr.example.com:9696/7/api",
  "apiKey": "dummy-api-key",
  "name": "tracker-a",
  "tracker": "tracker-a",
  "maxDownloadsPerDay": 20
}
```

```
necromanciarr config item set torznab tracker-a --element-stdin < tracker-a.json
necromanciarr config set prowlarr --value-stdin < prowlarr.json
necromanciarr config set sourceOrder '["provider", "torznab:tracker-a"]'
```

`prowlarr.json` holds the JSON string `"https://prowlarr.example.com:9696?apikey=dummy-api-key"`.

| Key | Default | Validation |
|---|---|---|
| `torznab[].url` | required | `http(s)`, no fragment, no user credentials, no query parameter, no `proxy` or `{proxyKey}` segment (`torznab url: a proxy path segment is not supported`); in clear |
| `torznab[].apiKey` | required | the indexer key, added to the URL as `apikey`; a secret, sealed in the database and masked everywhere (`config check`, logs, `doctor`) |
| `torznab[].name` | required | `[a-z0-9-]+`, unique; an element without `name` blocks loading (`a "name" is required`) |
| `torznab[].tracker` | empty | tracker name as the library provider writes it; used for the `.torrent` fallback and for the uniqueness (infohash, tracker); kept as written (trimmed), compared in lower case without spaces (`Tracker A` equals `trackera`), like the `trackers` keys |
| `torznab[].categories` | those of the indexer capabilities (films, series) | positive integers |
| `torznab[].minInterval` | `3s` | Go duration from `1s` to `1h`: minimum delay between two requests to this indexer (the `1s` floor protects the indexer from being limited or banned) |
| `torznab[].timeout` | `30s` | Go duration from `1s` to `5m` |
| `torznab[].maxQueriesPerRun`, `maxDownloadsPerRun` | `30`, `10` | integers from 1 to `100000` |
| `torznab[].maxQueriesPerDay`, `maxDownloadsPerDay` | `200`, `50` | integers from 1 to `100000` |
| `prowlarr` | empty | URL of the native API of Prowlarr (`http(s)://host:9696[/base]?apikey=KEY`, same rules as `torznab[].url`, sealed in the database); only for `doctor` (indexer list and state), never to search |
| `sourceOrder` | `provider`, then the `torznab` entries in list order | `provider` must stay first (a misplaced or missing `provider` is refused, never reordered); every source is `provider` or `torznab:<name>` of a configured indexer, listed once |

An indexer requires `NECROMANCIARR_SECRET_KEY`: without it the configuration is refused ("torznab requires NECROMANCIARR_SECRET_KEY"), because an indexer `.torrent` carries the tracker passkey and is only kept sealed ([03](03-seed-safety.md#passkeys-of-indexer-torrents)). Generate the key with `necromanciarr api-key --secret`.

An indexer absent from `sourceOrder` is never queried (`doctor` warns with `torznab_not_in_source_order`). The `torznab[].apiKey` key is entered as is; in `prowlarr`, a key that contains `&`, `#`, `%` or `;` must be percent-encoded in the URL (`%26`, `%23`, `%25`, `%3B`): `?apikey=ab%26cd` stands for `ab&cd`. A refused URL gets a generic message that never repeats it.

With Prowlarr, give the Torznab feed of each indexer (`/<id>/api`, the `<id>` being the indexer id in Prowlarr) and keep its `Redirect` option disabled (Proxy mode): with a redirect, the download links point to the original tracker and are refused. `doctor` warns when a `torznab` entry matches no enabled indexer of `prowlarr`.

## Tracker announces (`trackers`)

The `.torrent` of the library provider has no announce; to seed a private torrent, qBittorrent needs the tracker announce, which carries the operator passkey. The `trackers` section gives it for each tracker, to inject it at seed time (outside the `info` dictionary: the infohash does not change). `trackers` is required to seed provider torrents (they carry no announce). Seed injects it in memory, just before the client add; without an announce, a proposal is `no_announce` ([03-seed-safety.md](03-seed-safety.md)).

Two elements, one with a single announce, the other with several addresses and aliases (dummy passkeys):

```json
{"key": "tracker-a", "announce": "https://tracker-a.example.com/announce/dummypasskey0001"}
```

```json
{
  "key": "tracker-b",
  "announce": [
    "https://tracker-b.example.com/dummypasskey0002/announce",
    "udp://tracker-b.example.com:6969/announce?pk=dummypasskey0002"
  ],
  "aliases": ["TrackerB", "tracker-b.example.com"]
}
```

```
necromanciarr config item set trackers tracker-b --element-stdin < tracker-b.json
```

| Rule | Value |
|---|---|
| Key | `key`, tracker name, compared with the `tracker` of a candidate and with `torznab[].tracker` in lower case without spaces, then with the `aliases` in the same form. Two entries whose key or alias match in this form are refused: the message names both keys, never a URL. Entries are sorted by key |
| Value | `announce`: a URL or a list of URLs, never empty; `aliases`: a list of names, optional; any other key is refused; several URLs form a single `announce-list` tier (same tracker, several addresses) |
| URL | `http`, `https` or `udp`, host required, no fragment (`#`, even empty), no space (U+00A0 included) or control character, at most 2048 bytes, at most 512 URLs per entry; a refused URL gets a generic message that never repeats it |
| Secret | `announce` is sealed in the database. On load, every decoded path segment and parameter value of an announce becomes a known secret, except an ordinary word in lower case, upper case or capitalized (`announce`, `Announce`, `tracker-b`): at least 8 characters long, masked everywhere in their raw, upper-case, lower-case, percent-encoded and JSON-escaped forms; shorter (a `uid` of a few digits), masked only inside URLs, compared exactly (after decoding, case ignored), never in free text, where they would cut ordinary words. The whole URL is also rendered as `scheme://host[:port]/***` |
| `config check` | `trackers` lists `key`, `aliases`, `announce` (each URL as `https://tracker-a.example.com/***`), never a passkey |

A passkey that contains `&`, `#`, `%` or `;` must be percent-encoded in a query, as a `torznab` key.

## Library provider

HTTP contract defined in [`docs/contract/openapi.yaml`](../../contract/openapi.yaml), under the `libraryApi` base: `GET /v1/releases` (filters `tmdb`, `imdb`, `tvdb`, `q`, `size` repeatable 50 times, `tracker`, `limit` ≤ 100, cursor pagination), `GET /v1/releases/{infohash}`, `…/torrent`, `…/nfos`. Authentication `Authorization: Bearer <token>`, errors `application/problem+json`, `429` with `Retry-After`. A provider that ignores `size` works: a local defensive filter discards releases with no file of the right size, but it downloads many more candidates (`doctor` flags it).

## Diagnostics (`doctor`)

`necromanciarr doctor [--check ID]… [--json]`, and on the web side `POST /api/doctor/run` (body `{"checks": […]}`) or `POST /api/doctor/checks/{id}`: the diagnostic reaches the services and writes probe files in the link directories, so it is never served as a `GET` (action `doctor` marked as modifying in the registry). Each check is bounded to 10 seconds and returns no secret. The capabilities outcome of a Torznab indexer, successful or not, is kept 5 minutes by the server (a configuration change invalidates it): running `doctor` again within that time does not query the indexer again. Statuses: `ok`, `warn`, `skipped`, `fail`. The exit code is 1 if there is at least one `fail`, 0 otherwise. With an invalid configuration, `doctor` returns a single failed `config` check instead of stopping.

| Identifier | Checks | `fail` when |
|---|---|---|
| `config` | loading, database included; origin of each list (`none`, `cross-seed`, `database`) | invalid configuration, database included (unknown setting, unreadable value or element, sealed secret that does not open) |
| `config-dir` | existing folder, database openable for read-write | missing folder, database not openable, database in the library |
| `auth` | loopback listening, or token present | outside loopback without `NECROMANCIARR_AUTH_TOKEN` |
| `library-roots` | readable root, a media file found in the first 2000 entries | root missing, not a folder or unreadable; `warn` if no root or no media found |
| `library-api` | authentication, search, `size` filter | access denied, unknown path, unavailable; `warn` if `size` is ignored |
| `arr:radarr:<rang>`, `arr:sonarr:<rang>` | instance reachable, key accepted | unreachable, key refused |
| `torznab:<name>` | capabilities (`t=caps`, no search or download counted), key accepted, a movie or series search mode | key refused, unreachable, hostile response; `warn` if neither `movie-search` nor `tv-search` |
| `prowlarr` | native API: indexer list | key refused; `warn` for a `torznab` entry that is absent or disabled in Prowlarr |
| `qbittorrent` | connection, list, read-only mode, discarded clients | connection or credentials refused; `warn` for an unsupported client |
| `link-feasibility` | real probe of the link kinds in each link directory and torrent root; volume comparison with each library root (without writing anything in the library) | no wanted kind accepted, hardlink or reflink across different volumes; `warn` if only a symlink crosses |
| `fs-providers` | rc/binary mode, fingerprints, modification date, symlinks | provider cannot be built; `warn` with advice |
| `cross-seed` | `config.js`, daemon, effective mode | invalid mode; `warn` if daemon unreachable |
| `path-mappings` | each rule exists and covers a root | `warn` only |
| `dedupe` | torrent roots outside the library | root that overlaps the library |

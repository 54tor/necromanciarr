# Commands and web interface

The CLI and the web call the same layer (`app.Service`). Every web action has its command, every action command has its route and its button: a test fails if a route has no action, an action no command, or an action no button.

## Commands

Syntax: `necromanciarr <command> [arguments] [options]`. Positional arguments come **before** the options. Value options: `--name value` or `--name=value`. Any option the command does not know is passed on to the configuration (`--config-dir`, `--db-path`, `--listen`, `--language`, `--library-api`, `--read-bytes-per-second`, `--cross-seed-config`, `--cross-seed-mode`), which refuses what it does not know.

| Command | Effect | Options |
|---|---|---|
| `scan`, `match`, `verify`, `link`, `seed` | runs a stage ([01-pipeline.md](01-pipeline.md)) | |
| `status` | candidates by state, proposals by decision, application steps, effective cross-seed mode, verified torrents not proposed (`held`, one `held:` line per torrent with the reason) | |
| `proposals list` | lists the proposals | `--kind K`, `--state S`, `--group G`, `--source S`, `--current-only` |
| `proposals accept <id>` | accepts a torrent proposal | `--allow-partial-pack` |
| `proposals accept-group <groupe>` | accepts the group's primary (and the `cross_seed` ones in `native` mode) | |
| `proposals reject <id>` | rejects (from `pending` only) | |
| `proposals choose <id>` | makes a `full` proposal the primary of its group | |
| `proposals force <id>` | starts a paused `partial_annex` ([03-seed-safety.md](03-seed-safety.md)) | `--yes` required |
| `dedupe plan` | proposes links for torrent-side copies | |
| `dedupe apply <id>` or `--all-accepted` | accepts (if pending) and applies a proposal; or applies, one by one, all the accepted ones | `--dry-run`, `--all-accepted` |
| `repair scan` | proposes the new target of broken links | `--dir D` |
| `repair apply <id>` or `--all-accepted` | same as `dedupe apply` | `--dry-run`, `--all-accepted` |
| `links list` | lists the recorded links with their current state, read-only | `--state S`, `--limit N`, `--offset N` |
| `doctor` | diagnostics ([02-configuration.md](02-configuration.md)) | `--check ID` (repeatable) |
| `sources list` | the source chain in its order, without any network request | |
| `sources test <nom>` | tests an indexer: `t=caps` and one empty search, counted in its daily quota | |
| `serve` | serves the interface and the API until SIGINT or SIGTERM | |
| `healthcheck` | probes `/healthz` on loopback | |
| `api-key` | prints a new random token; `--secret`: a sealing key | |
| `config check` | prints the effective configuration, secrets masked | |

Values: `--kind` among `full partial_annex partial_pack cross_seed conflict dedupe repair`; a proposal's `--state` among `pending accepted rejected applied failed superseded`; a link's `--state` among `ok diverged lost unreadable`; `--limit` is 200 by default and 1000 at most, `--offset` starts at 0; `--source` is `provider` or `torznab:<nom>`. An unknown value is a usage error (exit 2), never an empty list.

Each proposal carries `details.source`, the source that supplied its candidate (`provider` or `torznab:<nom>`, never a URL); `--source` (web parameter `source`) keeps only the proposals of that source.

`sources list` returns one row per source in chain order (`sourceOrder`, the provider always first), then the configured indexers outside `sourceOrder` (`order` 0): `order`, `source`, `type` (`provider` or `torznab`), `tracker` (the declared `torznab[].tracker`; a URL-shaped value is rendered only as `scheme://hôte/***`), `state`, `queries` and `downloads` (`{"used", "cap"}`: usage over a sliding 24 h against `maxQueriesPerDay` and `maxDownloadsPerDay`; `null` for the provider, which has no quota) and `last_error`. `state` is `ok`, `blocked` (a received `Retry-After` sets the indexer aside), `exhausted` (a daily cap is reached) or `disabled` (outside the chain: no `libraryApi`, or missing from `sourceOrder`). `last_error` is the code of the indexer's last failed request, empty after a success: `indexer_unavailable` (unreachable, timeout, error status), `indexer_access_denied`, `indexer_hostile_response` (hostile or unreadable answer), `indexer_budget_exhausted`, `foreign_download_host`; never the error text nor a URL. The CLI prints the translated label in parentheses. The `blocked` state and `last_error` live in the process that made the requests (the server for the interface); a CLI command, a fresh process, only sees the usage and caps, read from the database.

`sources test <nom>` accepts `torznab:<nom>` or the bare indexer name. It goes through the budget like Match: every request counts against the daily quota, an indexer set aside or at its cap is not contacted. Capabilities still cached in the process (24 h) are not requested again. Result: `{"source", "result": "ok"|"failed", "error"}`, `error` carrying one of the `last_error` codes; a failed test exits with code 1. The provider is not tested here (`refused`, see `doctor --check library-api`), an unknown name is a `not_found`, an indexer outside the chain a `not_configured`.

`dedupe apply <id>` and `repair apply <id>` require the right kind (`wrong proposal kind`) and an applicable proposal (`ambiguous` and `lost` are not). Without `--dry-run`, a pending proposal is accepted then applied; with `--dry-run`, all checks run without creating anything. `--all-accepted` only applies the already accepted proposals of the kind, continues after a failure and combines with `--dry-run`.

### Exit codes

| Code | Meaning |
|---|---|
| 0 | succeeded; warnings are one `warning: …` line per warning on stderr (a stage that has failed items exits 0 with `N item(s) failed`) |
| 1 | failure: stage already running, component not configured, link inspection running, I/O error, at least one `doctor` `fail`, `healthcheck` unavailable |
| 2 | usage: invalid command, option or identifier, or missing confirmation (`proposals force` without `--yes`) |
| 3 | stale proposal (`stale_proposal`) |
| 4 | action refused by a rule (conflict, partial pack without option, proposal not accepted, outside roots, read-only client…) |

With `--all-accepted`: 3 if a proposal was stale, otherwise 1 if one failed.

### `--json` output

`--json` is accepted by all action and read commands (not by `serve`, `healthcheck`, `api-key`, `config check` which has its own JSON output). stdout carries indented JSON, sorted by key, secrets masked; warnings are already in it and are not repeated on stderr. Empty lists are `[]`, never `null`. An error is `{"error": {"code": "...", "message": "..."}}` on stdout, with the same exit code. Fields are in `snake_case`.

`proposals accept|reject|choose` answer `{"proposal_id", "action"}`; `accept-group` `{"group_id", "accepted": [...]}`; `dedupe apply` and `repair apply` an object `{proposal_id, applied, dry_run, link_target, bytes_read, warnings}`, or an array of these objects (with `error` per failed element) for `--all-accepted`.

### `serve`, `healthcheck`, `api-key`

- `serve` writes the log line `server_listening` (`listening on http://<address>`, translated) on stdout and, outside loopback, the line `non_loopback_warning` (`listening outside loopback, every api request needs the bearer token`, translated) on stderr. `serve` refuses to start outside loopback without `NECROMANCIARR_AUTH_TOKEN`, before opening the database; the other commands, `healthcheck` included, load the same configuration without a token: `listen "0.0.0.0:8787" is not loopback: set NECROMANCIARR_AUTH_TOKEN (at least 32 characters, generate one with necromanciarr api-key)`. Clean shutdown on SIGINT/SIGTERM: in-flight requests finished within 10 s, jobs cancelled.
- `healthcheck` loads the same configuration, queries `GET /healthz` on the listen host (`0.0.0.0`, `::` or an empty host are probed through `127.0.0.1`), 3 s at most, with no proxy or redirection. It prints `ok` (exit 0, identical in both languages) or the line `healthcheck_failed` (`healthcheck: unavailable`, translated, exit 1) and nothing else.
- `api-key` draws 256 bits of randomness, prints them as unpadded base64url (43 characters) on stdout and reminds on stderr to put them in `.env`. Nothing is written to disk or logged. `api-key --secret` does the same for `NECROMANCIARR_SECRET_KEY` (32 bytes, see [03](03-seed-safety.md#passkeys-of-indexer-torrents)).

## Web server

### Authentication

`NECROMANCIARR_AUTH_TOKEN` (at least 32 characters) is the only source of the token: `necromanciarr api-key` generates one to copy into `.env`.

| Listening | Token | `Host` check |
|---|---|---|
| loopback (`127.0.0.1`, `[::1]`, `localhost`) | optional; without a token, the API is open to any local process | yes |
| other address (`0.0.0.0`, network IP) | **required** when `serve` starts (not for one-off commands) | no: the token protects whatever the name, behind a proxy in particular |

- The token is only read from `Authorization: Bearer <token>` (scheme case-insensitive), never from the URL or a cookie. SHA-256 fingerprints compared in constant time. A refused token: 401, `WWW-Authenticate: Bearer`, a log line `api: unauthorized request from <address>`, with no rate limit.
- A token defined on a loopback listener is required too.
- `Host` must be the listen address, `localhost`, `127.0.0.1` or `[::1]` on the same port (anti DNS rebinding); otherwise 403 `unknown host header`.
- `Sec-Fetch-Site: cross-site`: 403 `cross-site request refused`, all methods.
- `POST`: `Content-Type: application/json` required (415 otherwise); if `Origin` is present it must be the request's own (403 otherwise); without `Origin` and without a configured token, `X-Requested-With: necromanciarr` is required. Body of at most 64 KiB (413), strict JSON: an unknown field or text after the object gives 400 `invalid json body`; an empty body counts as an empty object.
- No CORS header is set. Every response carries `X-Content-Type-Options: nosniff`, `Referrer-Policy: no-referrer`, `X-Frame-Options: DENY`, `Cross-Origin-Opener-Policy: same-origin` and a CSP `default-src 'self'` (no inline or external script, style or resource). The API adds `Cache-Control: no-store`.
- `GET /healthz` is public, with no check: `200 ok` or `503 unavailable` depending on the database's answer (2 s). It returns neither version nor configuration.
- Server timeouts: headers 10 s, read 30 s, idle 120 s, headers of at most 16 KiB.

### Routes

| Method | Path | Action | Body / parameters | Response |
|---|---|---|---|---|
| GET | `/api/status` | `status` | | 200 |
| GET | `/api/proposals` | `proposals list` | `decision`, `kind`, `group`, `current_only`, `source` | 200, array; each proposal carries `updated_at` (RFC 3339 UTC: the most recent of its creation, its decision and its last application step; empty if unknown) |
| POST | `/api/proposals/{id}/accept` | `proposals accept` | `{"allow_partial_pack": bool}` | 200 |
| POST | `/api/proposals/{id}/reject` | `proposals reject` | | 200 |
| POST | `/api/proposals/{id}/choose` | `proposals choose` | `{"candidate_id": n}` (the proposal's candidate or a member of its conflict) | 200 |
| POST | `/api/proposals/{id}/force` | `proposals force` | `{"confirm": true}` | 200 |
| POST | `/api/groups/{id}/accept` | `proposals accept-group` | | 200 |
| POST | `/api/stages/{stage}/run` | `run` | `stage` among `scan match verify link seed` | 202, job |
| POST | `/api/dedupe/plan` | `dedupe plan` | | 202, job |
| POST | `/api/dedupe/{id}/apply` | `dedupe apply` | `{"dry_run": bool, "accept_pending": bool}` | 202, job |
| POST | `/api/repair/scan` | `repair scan` | `{"directory": "..."}` | 202, job |
| POST | `/api/repair/{id}/apply` | `repair apply` | `{"dry_run": bool, "accept_pending": bool}` | 202, job |
| GET | `/api/links` | `links list` | `state`, `limit`, `offset` | 200, page |
| POST | `/api/doctor/run` | `doctor` | `{"checks": ["config", …]}` (empty: all) | 200, report |
| POST | `/api/doctor/checks/{checkId}` | `doctor` | `{}` | 200, report |
| GET | `/api/sources` | `sources list` | | 200, array |
| POST | `/api/sources/{name}/test` | `sources test` | `{}` | 200, test result |
| GET | `/api/jobs`, `/api/jobs/{id}` | job tracking (outside the registry) | | 200 |
| GET | `/healthz` | probe | | 200 or 503 |

A job whose action panics becomes `failed` with `internal error`; the panic value is only written masked, in the server log, and the server stays up. An unknown query parameter is a 400 `usage`. An unknown path under `/api/` is a 404 `not_found`; a wrong method on a known path, a 405 with `Allow`. The web `accept` answers `"action": "accepted"` where the CLI answers the command name. The web `dedupe apply` applies one proposal; the web has no `--all-accepted`: the interface chains the jobs from the browser, one per accepted proposal.

### Jobs

Long actions (stages, dedupe, repair) answer 202 with a job `{id, action, state, started_at, finished_at, result, error}`, `state` being `running`, `done` or `failed`. Only one job runs at a time: a second gets 409 `stage_running` (`another long action is running`); the stage lock in the database additionally protects against another process. The last 50 jobs are kept in memory and lost on shutdown; the durable result is in the database. A job runs in the server's context: a shutdown cancels it.

### Errors

Body `{"error": {"code", "message"}}`. The message is that of the recognized typed error; an unknown error gives `internal error` and the masked detail is only written in the server log.

| Code | HTTP | CLI | Case |
|---|---|---|---|
| `usage` | 400 (415 content type, 413 body) | 2 | invalid request or value |
| `confirmation_required` | 400 | 2 | `force` without `confirm` |
| `unauthorized` | 401 | | missing or wrong token |
| `forbidden` | 403 | | `Host`, `Origin`, `Sec-Fetch-Site`, `X-Requested-With` |
| `not_found` | 404 | 1 | proposal, group, job, route |
| `method_not_allowed` | 405 | | |
| `stale_proposal` | 409 | 3 | proposal from a stale generation, candidate became `stale` |
| `not_configured` | 409 | 1 | missing component (provider, client, `dedupe.torrentRoots`) |
| `stage_running` | 409 | 1 | another stage or job is running |
| `inspection_busy` | 409 | 1 | a link inspection is still running |
| `refused` | 422 | 4 | business rule: conflict to choose, partial pack, proposal not accepted, not applicable, wrong kind, inconsistent group, read-only client, outside roots… |
| `failure` | 500 | 1 | untyped error |
| `shutting_down` | 503 | | the server is stopping |

## Interface

Vue application embedded in the binary, a single anthracite dark theme, fragment routing (`#/…`): the server never sees the tab. Any path without an extension serves `index.html`. Without a built interface (`make ui` not run), a page "Interface not built" ("Interface non construite" in French, following `language`, with a matching `<html lang>`) is served and the API stays available.

| Tab | Fragment | CLI equivalent |
|---|---|---|
| Getting started | `#/demarrage` | `doctor`, `status` |
| Proposals (default) | `#/propositions` | `proposals list/accept/reject/choose/accept-group/force` |
| Deduplication | `#/deduplication` | `dedupe plan/apply` |
| Links | `#/liens` | `links list`, `repair scan/apply` |
| Stages | `#/etapes` | `scan match verify link seed`, job tracking |
| Sources | `#/sources` | `sources list/test` |

- Layout: sidebar on the left (icons only from 768 to 1023 px, drawer below), toolbar per page, dense sortable tables, state badges (icon and text, never colour alone). Below 768 px each row becomes a stacked card, with no horizontal scrolling.
- Links: two tabs. "Recorded links" is paginated by the server (200 per page, `links list` with `--limit` and `--offset`), filterable by state, unsorted (server order: `links list` has no sort parameter, and sorting only the received page would give a misleading order, hence no sort by state by default); "Not compared" marks a link of which nothing was compared, and an interrupted inspection (`truncated`) is announced. "Repairs" lists the `repair` proposals; the real application of a row requires typing the confirmation word (`APPLY` in English, `APPLIQUER` in French), the dry run starts directly; ambiguous, lost rows or rows waiting for a client recheck are listed but not applicable.
- Sources: one row per source in chain order (order, source, type, tracker, state, 24 h queries and downloads against the cap, translated last error), unsorted. "Test a source" tests the indexer chosen in the toolbar, the row button the row's one; tests are disabled while one runs, and its translated result is shown afterwards. The provider has no test button. Proposals: the Source column and the Source filter (applied by the server) are always shown.
- A dialog asks for the token on a 401; it lives in the tab's memory, never in browser storage. A refused token erases it.
- The interface redirects to "Getting started" at first launch (no candidate or proposal) or while the diagnostic has a `fail`, unless "Skip" is chosen.
- Force is only active for a `partial_annex` at the step `seeded_paused_incomplete`, with confirmation; accepting a `partial_pack` goes through a confirmation. `dedupe` and `repair` have a "Dry run" (dry-run) checkbox, checked by default, and a confirmation before really applying all the accepted proposals (outside a dry run). The Deduplication page goes further: "Apply accepted" always opens a window that lists the proposals (the mode, dry run or real, is frozen when it opens); a real replacement, bulk or of a single row, is only sent after typing the word `APPLY` (`APPLIQUER` in French); a single-row dry run starts without confirmation. The bytes to read are shown before any acceptance, the non-applicable rows (ambiguous, isolated copy…) stay listed with a disabled button and their reason.
- Language of the rendered texts: `language` (`fr` by default, or `en`) sets the language of the texts meant for the operator. Error codes, reasons (`reason`), states, and the names of JSON fields, commands and options stay in English and never change with the language; only the texts change: the `message` of errors (CLI `--json` and API), the CLI's text output and its usage help, the `description` of the action registry. `GET /api/status` carries `language`, which the interface reads before any rendering. An untyped error has a fixed message: "internal error (details in the server log)" on the API side, whose detail goes to the log; the CLI, which has no log, writes "internal error" followed by the detail (in English) in parentheses. The CLI's `error:` and `warning:` prefixes stay in English: they are markers readable by a script. The `doctor` report (CLI `--json` and `POST /api/doctor/…`) carries `id`, `status`, `code` (stable) and `title`, `detail`, `hint` (translated). Proposal warnings (`GET /api/proposals`, `proposals list`, results of `force`, `dedupe plan` and `repair scan`) are objects `{"code", "text"}`: `code` is the message's stable key (`warning.sources_changed`, `warning.application_failed.cross_device`…, `legacy.text` for a line recorded before the catalogue, rendered as is), `text` its rendering in the language. Reasons keep their code in `reason` and `reasons`; the translated label is added alongside: `reason_labels` (`{code: label}`) next to `reasons` in a stage result, `label` next to `reason` in `held` of `status`, in `excluded_files` of a proposal and in the dedupe and repair details; a code without a label is rendered as is. The CLI's text output shows the label in parentheses after the code. The reason codes of `links list` are stable (`link_missing`, `link_target_missing`, `link_inode_diverged`, `link_target_renamed`, `link_not_compared`, `link_replaced`, `link_resized`, `link_target_unreadable`, `link_inspection_failed`); `label` carries their translated label, and the CLI shows `code (label)`. The reasons of a dedupe proposal without replacement (`details.reason`) are `isolated_copy`, `hash_unavailable`, `no_link_kind`, `cross_device`, `link_kind_unsupported` and `link_probe_failed`, with `details.label`; `details.warnings` lists the full keys of the plan's warnings (`warning.dedupe.unreliable_inode`…, `warning.no_client_recheck`; the short value `no_client_recheck` of old lines is still read), whose text is in the proposal's `warnings`; the `maintenance` detail of a proposal (`GET /api/proposals`) carries the same `label`. `warnings` of `status`, of a stage result (cross-seed mode, `warning.crossseed.*`, `warning.multiple_clients`) and of the result of `dedupe apply` and `repair apply` are objects `{"code", "text"}`. The diagnostic's mount provider advice (`advice.mount.*`) is rendered in the language. Lines already recorded before this version keep their original text (`legacy.text`; an old `details.reason` is an English sentence rendered as is).
- Not yet translated, each for a specific reason: the parenthesized detail of an untyped error in the CLI, and the `error` parameter of log lines and of `warning.application_failed.detail` (`err.Error()` text of the low layers: paths, service responses, no stable key to translate; it is masked before display); the probe reason of a link in the `✓`/`✗` marks of the `link-feasibility` diagnostic (English technical text from the kernel or the filesystem); the `error` parameters of the diagnostic (`doctor`) for the same reason; the dedupe plan warnings recorded by an earlier version (`legacy.text`), which cannot be found under a key; the detail of the text of usage errors after their typed prefix.
- Server log: `slog` lines in text format on stderr (`time=… level=… msg="…" code=…`). `msg` is rendered in the configured language, `code` is stable and never translated: `job_failed`, `job_panicked`, `server_listening`, `non_loopback_warning`, `healthcheck_failed`, `api_internal_error`, `api_unauthorized`, `api_panicked` (panic of a synchronous handler: generic internal error response, value and stack never returned) and `http_server_error` (lines of `http.Server.ErrorLog`). Each parameter value (error detail, address) is masked before rendering. `server_listening` goes to stdout (`msg="listening on http://<address>"`), the others to stderr; `healthcheck` writes the stable word `ok` on stdout (the same in both languages: a script reads it like `error:` and `warning:`), or the line `healthcheck_failed` on stderr (exit code 1). A failure at `serve` startup always writes the plain CLI line `error: …`, with no `code`.
- In text output, the CLI strips from everything it prints the control characters (ESC, OSC and CSI sequences, C1, NUL…), except line feed and tab: a torrent name or a service response cannot drive the terminal. `--json` output is not affected (JSON escapes these characters).

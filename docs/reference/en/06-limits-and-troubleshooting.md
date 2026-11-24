# Limits and troubleshooting

## v1 limits

### Reading and remote storage

- Verify reads **every byte** of the local files covered by a candidate's pieces. The only brake is `readBytesPerSecond` (20 MiB/s by default, burst 1 MiB); coverage has no byte cap per candidate (`tieBreakReadBytes` only bounds the tie-breaking of same-size copies). Estimated duration of a candidate: file size divided by the rate. Pieces already verified are not reread on resume, but any invalidation (modified file, changed associations) starts again from zero.
- Verify processes one candidate at a time, with no parallelism.
- On a mounted object store (rclone, S3) a full read costs time, bandwidth and, with some providers, billed transfer. S3 provides an MD5 but no SHA-1: piece verification always rereads the files; only deduplication and repair avoid reading through `fsProviders`.
- `dedupe plan` announces `bytes_to_read` before acceptance: 0 if the storage gives both fingerprints, the size of one file if one is needed, both otherwise. Start-end fingerprints (2 MiB) are cached by (path, size, date).
- A mount that does not return the file's modification date (classic `mega` backend) disables the "size and date" method of repair; a rewritten object changes date and makes the affected candidates stale at the next scan.
- A rename over a file is not atomic on some volumes (rclone's VFS can delete the destination before renaming): deduplication writes this as a warning and `dedupe plan` picks up the remaining temporary entries.
- A `Force` on a `partial_annex` copies into the link directory each media file straddling a missing piece: without reflink, this costs the media file's size in free space (and in reads on remote storage).
- Scan only inventories media files, follows no symlink inside a root and ignores a subfolder mounted on another filesystem: declare each mount as its own root in `libraryRoots`.

### Volume types

`linker` classifies a path's volume by `statfs` (Linux): `local`, `fuse` (rclone, sshfs…), `network` (NFS, SMB/CIFS, 9p, Ceph, AFS, Lustre, GFS2, OCFS2, Coda, NCP, OrangeFS) or `unknown` (other platforms). Only `local` has stable inodes: on the others, the inode is not kept in the database, repair "by inode" is impossible and deduplication adds the warnings `warning.dedupe.unreliable_inode`, `warning.dedupe.full_network_read` or `warning.dedupe.sampled_verify` (translated text in the proposal's `warnings`). On Windows, only a network drive or a UNC share is recognized as `network`; an rclone mount (WinFsp) shows up there as a fixed drive and is not detected.

### Links

| Kind | Constraint |
|---|---|
| hardlink | same filesystem as the source (otherwise `EXDEV`, reason `cross_device`); absent on FAT volumes and generally on a FUSE mount; number of links per file bounded by the system (`EMLINK`) |
| reflink | Linux only (`FICLONE`, btrfs, XFS with reflink); elsewhere `link_kind_refused`; same filesystem |
| symlink | **absolute** target as seen by this process (at Link): the client must see the same tree, and qBittorrent does not always follow symlinks; on Windows, privilege or developer mode required |

- A **relative** symlink only exists for `dedupe` (`relativeSymlinks`); with no possible relative path (distinct Windows volumes) the application fails, with no fallback to an absolute path.
- `linkType` designates a single kind: if the link directory refuses it (probe), the proposal fails; there is no fallback from hardlink, then reflink, then symlink at Link. `dedupe.kinds` is, for its part, an ordered list.
- The comparison of a reflink or a copy in `links list` and `repair scan` is done **by size alone** (`Lstat`), never by content; a copy of the same size but different content is `ok`. Without a recorded size, the link is `ok` with `compared: false` ("Not compared").
- A symlink **repointed** to another existing target is not detected.
- `links list` does not modify the links' state in the database (it stays that of the last `repair scan`). Each call re-examines all the links (20 s budget, one inspection at a time): a partial list (`truncated`) can give pages that do not join up exactly; a `Stat` blocked by a frozen mount keeps its goroutine until the kernel answers, hence `inspection_busy`.
- A single link directory per torrent: the first of `linkDirs` on the same filesystem as the first source file.

### Paths and case

- Windows: `pathMappings` rules with a drive letter or `\\` ignore case and accept `\` or `/`. The matching of Radarr and Sonarr paths with the inventory, however, is **case-sensitive** (`D:\Media` is not `d:\media`): a mismatch gives `arr_paths_matched_nothing`.
- The overlap checks between link directories, torrent roots and library are lexical, case-sensitive, then redone on resolved paths by Link and `dedupe`. A case-insensitive volume is not handled.
- A Windows client path that contains a forbidden character (`: ? * " < > |` after the drive) is refused (`client_path_invalid`).

### Straddling pieces and annexes

- A piece that covers an absent file is never counted as owned: a torrent complete on the media side stays `partial_annex` if an annex is missing and no `.nfo` variant verifies. The cap on missing pieces of a `partial_annex` is not configurable.
- A kept `.nfo` annex weighs at most 1 MiB; a kept `.torrent` at most 8 MiB. A `.torrent` whose piece length is zero, negative or exceeds 256 MiB is `invalid_torrent`.
- Torrent client responses: Seed queries the client by hash (`torrents/info?hashes=`), never the whole list; the diagnostic only reads one page ("torrents: at least 500" beyond that). The recheck of a path (dedupe, repair) reads the list **page by page** (`limit=500`, sort `sort=hash`, pages that overlap by one torrent) and keeps from each page only the torrents whose folder contains the file: only one page is in memory at a time, with no client size limit other than a safety bound of 200,000 torrents (`qbit: more torrents than the listing bound`). Each page is capped at 8 MiB (`qbit: response larger than its limit`), like the list of a torrent's files and the export of a `.torrent`; an unreadable or too large page, or a cancellation, stops the read, nothing is paused. qBittorrent has neither snapshot nor `save_path` filter: if the first hash of a page is no longer the last of the previous one (torrent added or removed during the read), the list is reread in full, three times at most, then `qbit: the torrent list kept changing while it was read`. The set of torrents to cover is read once before the pause; a torrent added after that read is not paused by this guard.

### Scope

- Only qBittorrent (and qui through its proxy) is supported; only the first client of `torrentClients` receives torrents. The `necromanciarr` tag is fixed; the category is `linkCategory`.
- Match only queries files that have a TMDB or TVDB identifier from Radarr or Sonarr. SFTP is not supported; Torznab and Prowlarr can be configured and `doctor` checks them, but Match does not query them yet.
- An accepted proposal cannot be cancelled; a rejected proposal is not reopened in the same generation. A `failed` candidate has no manual retry: it goes back to `matched` only if its associations change.
- Web jobs are not persisted; the web's `dedupe apply` and `repair apply` apply one at a time.
- The titles, details and hints of `doctor` follow `language`; its codes never change. The server's failure reasons stay in English until translated, and `doctor` shows absolute paths to the authenticated operator.

### API security

- **No rate limit on authentication**: a failure writes a log line (address, no token) and nothing else. Keep the token long (at least 32 characters, `api-key` draws 43) and do not expose the port without a proxy.
- **Optional token on loopback**: on a machine shared between several users, any local user reaches the `127.0.0.1` API without a token. Set `NECROMANCIARR_AUTH_TOKEN` even on loopback.

### Database and platforms

- **SQLite journal**: the database keeps the default journal mode (`DELETE`), without WAL. WAL mode requires shared memory (`-shm`) that network volumes (NFS, SMB, rclone mounts) do not guarantee, and the database often lives on such a volume in Docker; transactions take the write lock from `BEGIN` (`_txlock=immediate`) with a 5-second wait (`busy_timeout`).
- **Windows**: the `windows/amd64` and `windows/arm64` binaries are built; CI only runs on Windows the tests of the `internal/linker`, `internal/config` and `internal/pipeline` packages (job `windows-tests`, non-blocking until its first run is triaged), on `amd64`.

### Accepted risks (v1 review)

| Risk | Condition |
|---|---|
| `doctor` follows a symlink between the overlap check and the probe (TOCTOU) | a third party modifies the link directories during the diagnostic |
| At Link, adopting an already present entry does not replay the per-segment containment check | a third party has already written in the tool's link directory |
| Writing an annex renames over without an `fsync` of the parent folder | hard power cut at that instant |
| A duplicated `config.json` key is not detected (the last one wins) | `encoding/json` limitation |
| Development dependencies: 5 `npm audit` alerts of moderate level | dev only (the blocking threshold is `high`) |

## Troubleshooting

### By `doctor` check

Each result carries a stable `code` in snake_case, identical in French and English: a script relies on `code` and `status`, never on `detail` or `hint`, which are translated (`language`). A check that examines several items (roots, folders, clients, rules) carries the code of its worst finding, the first on equal status; all findings stay in `detail`, separated by "; ".

| Check | Code | Status | Cause and action |
|---|---|---|---|
| all | `ok` | `ok` | nothing to do |
| all | `check_timeout` | `fail` | the check exceeded its timeout (`WithTimeout`, 10 s by default): slow or blocked service |
| all | `cancelled` | `skipped` | diagnostic interrupted before this check |
| all | `panic` | `fail` | bug: the type and a masked message are given; report it |
| `config` | `working_dir_dotenv` | `warn` | a working-directory `.env` was read: move it to the configuration folder, or delete it |
| `config` | `config_invalid` | `fail` | invalid `config.json` (`unknown field …`, `parse config.json`, `undefined variable X`, `linkDirs[i] … overlaps libraryRoots[j]`): fix, then `config check` |
| `config-dir` | `config_dir_missing` | `fail` | configuration folder missing: create it or pass `--config-dir` |
| `config-dir` | `database_dir_unresolved` | `fail` | database folder not found: create it or fix `dbPath` |
| `config-dir` | `database_in_library` | `fail` | `dbPath` in a library root: put it elsewhere |
| `config-dir` | `database_unwritable` | `fail` | permissions or space on the database folder; in Docker, the owner of `/data` |
| `auth` | `loopback` | `ok` | listening on loopback |
| `auth` | `auth_token_set` | `ok` | listening outside loopback with a token |
| `auth` | `no_auth_token` | `fail` | listening outside loopback without a token, which `serve` will refuse: `necromanciarr api-key`, then `.env` |
| `library-roots` | `no_library_root` | `warn` | `libraryRoots` empty |
| `library-roots` | `root_not_found` | `fail` | mount missing or wrong path |
| `library-roots` | `root_not_directory` | `fail` | the root is not a folder |
| `library-roots` | `root_unreadable` | `fail` | permissions of the process user |
| `library-roots` | `root_empty` | `warn` | no media in the first 2000 entries: empty root or mount not ready yet |
| `library-roots` | `root_ok` | `ok` | readable root, media found |
| `library-api` | `library_api_unset` | `skipped` | `libraryApi` not configured |
| `library-api` | `provider_client_failed` | `fail` | `libraryApi` or `LIBRARY_PROVIDER_TOKEN` unusable |
| `library-api` | `provider_access_denied` | `fail` | `LIBRARY_PROVIDER_TOKEN` wrong or missing |
| `library-api` | `provider_not_found` | `fail` | base path of `libraryApi` |
| `library-api` | `provider_rate_limited`, `provider_unavailable` | `fail` | provider quota or outage; retry later |
| `library-api` | `provider_bad_response` | `fail` | response outside [`openapi.yaml`](../../contract/openapi.yaml) |
| `library-api` | `provider_timeout`, `provider_request_failed` | `fail` | network or provider; check `libraryApi` |
| `library-api` | `size_filter_ignored` | `warn` | provider without a `size` filter: works, many more candidates |
| `library-api` | `size_filter_unverifiable` | `ok` | no usable result to verify the filter |
| `arr:radarr:0` | `api_key_rejected` | `fail` | the instance's `?apikey=` |
| `arr:radarr:0` | `timed_out`, `unreachable` | `fail` | URL, instance started |
| `torznab:12` | `torznab_ok` | `ok` | capabilities read, key accepted (only `t=caps` is requested: no search nor download is counted) |
| `torznab:12` | `torznab_access_denied` | `fail` | the `?apikey=` of the `torznab` entry (HTTP 401 or 403, or Torznab error 100 to 199) |
| `torznab:12` | `torznab_unreachable` | `fail` | URL, indexer or Prowlarr started; quota reached (HTTP 429 or 503, Torznab errors 429, 500, 501, 503, 900, 910) |
| `torznab:12` | `torznab_hostile_response` | `fail` | response refused (`DOCTYPE` or `ENTITY`, nesting too deep, more than 500 items): the URL is not a trusted feed |
| `torznab:12` | `torznab_bad_response` | `fail` | not a Torznab feed: wrong path (Prowlarr: `/<id>/api`) |
| `torznab:12` | `torznab_not_in_source_order` | `warn` | the indexer is missing from `sourceOrder`: it is never queried; add it or remove it from `torznab` |
| `torznab:12` | `torznab_no_media_mode` | `warn` | neither `movie-search` nor `tv-search`: the indexer will give no candidate |
| `prowlarr` | `prowlarr_ok` | `ok` | indexer list read, every `torznab` entry that comes from Prowlarr is enabled there |
| `prowlarr` | `prowlarr_access_denied` | `fail` | the `?apikey=` of `prowlarr` (Settings > General > API Key) |
| `prowlarr` | `prowlarr_unreachable`, `prowlarr_bad_response` | `fail` | `prowlarr` is the root of the native API (`http://host:9696`, plus the URL base), not a feed; Prowlarr started |
| `prowlarr` | `prowlarr_indexer_missing_or_disabled` | `warn` | a `torznab` entry whose path id is missing or disabled in Prowlarr: enable it or remove it; downloading needs Prowlarr to proxy the file (Proxy mode, `Redirect` option disabled on the indexer) |
| `secret-key` | `secret_key_ok` | `ok` | `NECROMANCIARR_SECRET_KEY` set (32 bytes) and every sealed `.torrent` read from the database (at most 1000 blobs and 64 MiB per run; no database: none) unseals, its binding taken from the candidate row |
| `secret-key` | `secret_key_missing` | `fail` | `torznab` configured without `NECROMANCIARR_SECRET_KEY`: the configuration is refused; generate one with `necromanciarr api-key --secret` |
| `secret-key` | `secret_key_wrong_length` | `fail` | the value is not 32 bytes in base64url without padding (only the `api-key --secret` spelling is accepted) |
| `secret-key` | `sealed_torrent_unreadable` | `fail` | a sealed `.torrent` no longer unseals (key changed or blob altered): Seed will refuse it; run Verify again on this candidate |
| `secret-key` | `sealed_torrents_unchecked` | `warn` | the sealed `.torrent` files could not be read from the database (unreadable or from an earlier version), or some exceed the diagnostic budget (1000 blobs, 64 MiB) and are not checked; the key itself is valid |
| `qbittorrent` | `no_torrent_client` | `skipped` | no client configured |
| `qbittorrent` | `client_build_failed` | `fail` | client URL unusable |
| `qbittorrent` | `client_login_rejected` | `fail` | credentials in the URL; percent-encode `@ : / # ?` of the password |
| `qbittorrent` | `client_timeout`, `client_unreachable` | `fail` | URL, client started |
| `qbittorrent` | `client_list_failed` | `fail` | connection succeeded, torrent list refused |
| `qbittorrent` | `client_ok`, `seed_settings` | `ok` | client reachable; category and tags set on add |
| `qbittorrent` | `client_type_unsupported` | `warn` | client imported from cross-seed other than qBittorrent: ignored |
| `link-feasibility` | `no_link_directory` | `skipped` | neither `linkDirs` nor torrent root |
| `link-feasibility` | `link_dir_unresolved` | `fail` | link directory not found: no probe launched |
| `link-feasibility` | `link_dir_in_library` | `fail` | link directory in the library: move `linkDirs` |
| `link-feasibility` | `link_probe_failed` | `fail` | folder not writable |
| `link-feasibility` | `link_kinds_ok` | `ok` | wanted link kinds accepted (✓ accepted, ✗ refused, with the reason) |
| `link-feasibility` | `link_kind_fallback` | `warn` | the preferred kind is refused, a following one is accepted; with `symlink` refused on an rclone mount: restart rclone with `--vfs-links` |
| `link-feasibility` | `link_kinds_refused` | `fail` | no wanted kind is accepted |
| `link-feasibility` | `volumes_not_comparable` | `warn` | one of the two paths does not exist |
| `link-feasibility` | `same_volume` | `ok` | library and links on the same volume |
| `link-feasibility` | `volumes_differ_symlink_only` | `warn` | two volumes: only symlinks cross |
| `link-feasibility` | `volumes_differ` | `fail` | library and links under two mounts: a single common mount ([05-docker-and-volumes.md](05-docker-and-volumes.md)) |
| `fs-providers` | `no_fs_provider` | `skipped` | no provider configured |
| `fs-providers` | `fs_providers_build_failed` | `fail` | `fsProviders` of `config.json` |
| `fs-providers` | `mount_ok` | `ok` | mount described |
| `fs-providers` | `mount_advice` | `warn` | the provider's advice (`advice.mount.*` keys of `hashsource`, rendered in the language): start rclone with `--rc`, backend without MD5 or without date (the comparison rereads the files) |
| `cross-seed` | `cross_seed_unset` | `skipped` | cross-seed not configured |
| `cross-seed` | `cross_seed_mode_invalid` | `fail` | unknown `crossSeedMode` |
| `cross-seed` | `cross_seed_warning` | `warn` | daemon unreachable: `native` fallback in `auto` mode, disabled in `delegate` |
| `cross-seed` | `blocked_environment` | `warn` | `config.js` reads this tool's variables, which are hidden from it |
| `path-mappings` | `no_path_mapping` | `skipped` | no rule |
| `path-mappings` | `rule_unreachable` | `warn` | neither `from` nor `to` exists on this machine |
| `path-mappings` | `rule_no_library_root` | `warn` | rule that covers no root |
| `path-mappings` | `rule_ok` | `ok` | usable rule |
| `dedupe` | `dedupe_inactive` | `skipped` | `dedupe.torrentRoots` empty |
| `dedupe` | `torrent_root_overlaps_library` | `fail` | `torrentRoots` to move out of the library |
| `dedupe` | `torrent_root_missing` | `warn` | folder not found on this machine |
| `dedupe` | `torrent_root_ok` | `ok` | usable root |

### By stage reason (`reasons`) and failed proposals

| Reason | Stage | Cause and action |
|---|---|---|
| `no_external_id` | Scan | file absent from Radarr/Sonarr or path badly translated: check `radarr`, `sonarr`, `pathMappings` |
| `arr_paths_matched_nothing` | Scan | no path of the instance matches: wrong rule, Windows case |
| `empty_root_not_pruned` | Scan | all the files of a root are missing: detached mount? |
| `search_failed`, `update_deferred_incomplete_run` | Match | search failed: rerun `match` |
| `changed_file_stale`, `stale_candidate` | Match, Verify | file modified: rerun `scan`, `match`, `verify` |
| `media_hash_failed` | Verify | a present media file does not have the torrent's bytes (re-encoded, corrupted, other release): no link |
| `read_budget_exceeded` | Verify | tie-breaking of same-size copies stopped at `tieBreakReadBytes`: the copies stay in conflict; delete the duplicates or raise the value |
| `no_file_matched`, `no_exact_size` | Verify, Match | no local file of the right size |
| `v2_only`, `invalid_torrent`, `infohash_mismatch` | Verify | `.torrent` unusable or not the announced one |
| `torrent_not_found`, `torrent_fetch_failed`, `torrent_too_large` | Verify | provider: 404, outage, `.torrent` over 8 MiB |
| `read_error` | Verify | local read failed: resumes on rerun |
| `no_link_dir_on_same_device` | Link | no `linkDirs` on the source's volume; `linkType` is not `symlink` |
| `link_kind_refused`, `cross_device` | Link | the folder refuses the kind (see `doctor`), or two volumes |
| `destination_conflict` | Link | the torrent folder contains an entry that is not the tool's: nothing is deleted, look at it |
| `link_root_unusable`, `link_dir_overlaps_library` | Link | folder missing, not writable or in the library |
| `source_changed`, `changed_file_stale` | Link, Seed | source modified: verify again |
| `annex_write_failed`, `link_failed` | Link | write refused or system error: permissions, space |
| `not_linked` | Seed | proposal accepted but Link did not run |
| `already_in_client` | Seed | the infohash exists in the client at another path: nothing is touched |
| `client_read_only` | Seed | client declared `readonly:` |
| `add_failed`, `recheck_failed`, `resume_failed`, `pause_failed` | Seed | client unreachable or refusal: `doctor` (`qbittorrent`), rerun |
| `not_paused_after_add` | Seed | incomplete torrent not found, or started despite two pauses (then removed from the client, data kept): check the client, then re-accept and rerun Seed |
| `incomplete_after_recheck`, `paused_incomplete` | Seed | torrent left paused (see [03-seed-safety.md](03-seed-safety.md)) |
| `link_changed`, `annex_changed` | Seed | torrent folder modified since Link: re-accept and rerun `link` |
| `client_path_invalid` | Seed | client path with a forbidden character on Windows |
| `crossseed_notify_failed` | Seed | daemon refuses or unreachable: warning only |

### By error code

| Code | Cause and action |
|---|---|
| `stale_proposal` | stale generation: rerun `scan`, `match`, `verify` then decide on the new proposal |
| `refused` | read the message: conflict to `choose`, partial pack without `--allow-partial-pack`, `the proposal must be accepted first` for dedupe and repair, wrong kind, outside roots, read-only client |
| `not_configured` | missing component: `libraryApi`, `torrentClients` or `dedupe.torrentRoots` |
| `stage_running` | another stage or action is running; wait, or 2 minutes after the process dies |
| `pipeline: run lock lost` | the lock was taken over: rerun the stage |
| `inspection_busy` | a link inspection is running: retry |
| `confirmation_required` | `proposals force` without `--yes`, or `force` without `{"confirm": true}` |
| `unauthorized` | `Authorization: Bearer <token>` missing or wrong |
| `forbidden` | unknown `Host` (proxy that rewrites `Host` on a loopback listener), foreign `Origin`, `Sec-Fetch-Site: cross-site` |

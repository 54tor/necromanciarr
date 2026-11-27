# Limits and troubleshooting

## v1 limits

### Reading and remote storage

- Verify reads **every byte** of the local files covered by a candidate's pieces. The only brake is `readBytesPerSecond` (20 MiB/s by default, burst 1 MiB); coverage has no byte cap per candidate (`tieBreakReadBytes` only bounds the tie-breaking of same-size copies). Estimated duration of a candidate: file size divided by the rate. Pieces already verified are not reread on resume, but any invalidation (modified file, changed associations) starts again from zero.
- Verify processes one candidate at a time, with no parallelism.
- On a mounted object store (rclone, S3) a full read costs time, bandwidth and, with some providers, billed transfer. S3 provides an MD5 but no SHA-1: piece verification always rereads the files; only deduplication and repair avoid reading through `fsProviders`.
- `dedupe plan` announces `bytes_to_read` before acceptance: 0 if the storage gives both fingerprints, the size of one file if one is needed, both otherwise. Start-end fingerprints (2 MiB) are cached by (path, size, date).
- A mount that does not return the file's modification date (classic `mega` backend) disables the "size and date" method of repair; a rewritten object changes date and makes the affected candidates stale at the next scan.
- A rename over a file is not atomic on some volumes (rclone's VFS can delete the destination before renaming): deduplication writes this as a warning and `dedupe plan` picks up the remaining temporary entries.
- `dedupe` and `repair` read and write through the volume registry: every write (temporary link, replacement, removal, recovery by `dedupe plan`) goes through the write root of the torrent folder or link folder, refused if it overlaps the library. The replacement requires an atomic replace announced by the volume: without it (SFTP server without `posix-rename`), the dedupe proposal is not applicable (`no_atomic_replace`) and `repair apply` refuses, writing nothing and never falling back to a rename or a delete then create.
- On a remote volume (`sftp://`) without remote commands, dedupe has no storage fingerprint: `verify: auto` proves by a full read of both files, announced before acceptance by `bytes_to_read` and the `warning.dedupe.full_remote_read` warning; `verify: hash` gives `hash_unavailable`. With `RemoteHash` (`exec` probe), the hash comes from `sha1sum` or `md5sum` on the host (source `sftp-exec`): nothing crosses the network, the plan's `remote_hash_bytes` announces the bytes the server will read again (once per remote file) with `warning.dedupe.remote_hash_bytes`. A failed command gives `warning.dedupe.remote_exec_failed` in the plan; at apply time, `verify: auto` moves back to a full read (never to samples) and `verify: hash` refuses without reading. A remote file identity is never a proof: a file is not "already linked" without a known identity, and the temporary link is checked by the start-end fingerprint of the library file read back through it. Repair "by inode" is only tried on a volume that announces file identity (`FileIdentity` locally, `RemoteStat` remotely); found by a remote inode, it is never applied automatically (explicit acceptance). A remote identity is only compared with another one of the same volume. A hardlink whose identity is unknown is `ok` without comparison.
- A `Force` on a `partial_annex` copies into the link directory each media file straddling a missing piece: without reflink, this costs the media file's size in free space (and in reads on remote storage).
- Scan only inventories media files, follows no symlink inside a root and ignores a subfolder mounted on another filesystem (on every platform): declare each mount as its own root in `libraryRoots`.

### Volume types

`linker` classifies a path's volume by `statfs` (Linux): `local`, `fuse` (rclone, sshfs…), `network` (NFS, SMB/CIFS, 9p, Ceph, AFS, Lustre, GFS2, OCFS2, Coda, NCP, OrangeFS) or `unknown` (other platforms). Only `local` has stable inodes: on the others, the inode is not kept in the database, repair "by inode" is impossible and deduplication adds the warnings `warning.dedupe.unreliable_inode`, `warning.dedupe.full_network_read`, `warning.dedupe.full_remote_read` (`sftp://` volume), `warning.dedupe.remote_hash_bytes`, `warning.dedupe.remote_exec_failed` or `warning.dedupe.sampled_verify` (translated text in the proposal's `warnings`). On Windows, only a network drive or a UNC share is recognized as `network`; an rclone mount (WinFsp) shows up there as a fixed drive and is not detected.

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
- Match only queries files that have a TMDB or TVDB identifier from Radarr or Sonarr. An `sftp://` library needs a `pathMappings` rule whose `from` is its location, so that Radarr and Sonarr paths reach it.
- An accepted proposal cannot be cancelled; a rejected proposal is not reopened in the same generation. A `failed` candidate has no manual retry: it goes back to `matched` only if its associations change.
- Web jobs are not persisted; the web's `dedupe apply` and `repair apply` apply one at a time.
- The titles, details and hints of `doctor` follow `language`; its codes never change. Failure reasons carry a translated label (only the `err.Error()` parameters stay in English, see 04); `doctor` shows absolute paths to the authenticated operator.

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
| A duplicated key inside a database JSON value (setting or element) is not detected (the last one wins) | `encoding/json` limitation |
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
| `config` | `config_invalid` | `fail` | invalid configuration, database included (`unknown stored setting "x"`, `stored setting "x": unreadable value`, `stored <list> item "<id>": field <field>: ${...} references are not supported`, `stored torznab item "<id>": a "name" is required`, `sealed config secrets cannot be read: NECROMANCIARR_SECRET_KEY is missing or changed`, `linkDirs[i] … overlaps libraryRoots[j]`, `usage: libraryRoots[i]: … local path is not absolute` or `… contains ..`, `… provider "x" is not declared in fsProviders`, `… provider "x" has type "rclone", which does not expose a filesystem`, `fsProviders[i]: usage: type sftp: knownHosts is required …`, `usage: dbPath: the database stays on a local disk, never on an sftp:// location`): fix with `config set` or `config item set`, then `config check` |
| `config-dir` | `config_dir_missing` | `fail` | configuration folder missing: create it or pass `--config-dir` |
| `config-dir` | `database_dir_unresolved` | `fail` | database folder not found: create it or fix `--db-path` |
| `config-dir` | `database_in_library` | `fail` | `--db-path` in a library root: put it elsewhere |
| `config-dir` | `database_unwritable` | `fail` | permissions or space on the database folder; in Docker, the owner of `/data` |
| `auth` | `loopback` | `ok` | listening on loopback |
| `auth` | `auth_token_set` | `ok` | listening outside loopback with a token |
| `auth` | `no_auth_token` | `fail` | listening outside loopback without a token, which `serve` will refuse: `necromanciarr api-key`, then `.env` |
| `auth` | `auth_public_url_unusable` | `warn` | https `publicUrl` whose host is an IP address: an IP cannot be a WebAuthn identifier, so security keys and passkeys are unavailable; set `publicUrl` to the https host name users reach |
| `auth` | `auth_public_url_missing` | `warn` | listening outside loopback without an https `publicUrl`: the session cookie is not `Secure` and WebAuthn is disabled; set `publicUrl` to the https address users reach |
| `library-roots` | `no_library_root` | `warn` | `libraryRoots` empty |
| `library-roots` | `root_not_found` | `fail` | mount missing or wrong path |
| `library-roots` | `root_not_directory` | `fail` | the root is not a folder |
| `library-roots` | `root_unreadable` | `fail` | permissions of the process user |
| `library-roots` | `root_empty` | `warn` | no media in the first 2000 entries: empty root or mount not ready yet |
| `library-roots` | `root_ok` | `ok` | readable root, media found |
| `library-api` | `library_api_unset` | `skipped` | `libraryApi` not configured |
| `library-api` | `provider_client_failed` | `fail` | `libraryApi` or `libraryToken` unusable |
| `library-api` | `provider_access_denied` | `fail` | provider token (`libraryToken`) wrong or missing |
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
| `link-feasibility` | `library_root_unavailable` | `fail` | a library root is missing or not mounted (or none is configured): no write root is opened, neither probe nor link |
| `link-feasibility` | `link_kinds_ok` | `ok` | wanted link kinds accepted (✓ accepted, ✗ refused, with the reason) |
| `link-feasibility` | `link_kind_fallback` | `warn` | the preferred kind is refused, a following one is accepted; with `symlink` refused on an rclone mount: restart rclone with `--vfs-links` |
| `link-feasibility` | `link_kinds_refused` | `fail` | no wanted kind is accepted |
| `link-feasibility` | `volumes_not_comparable` | `warn` | one of the two paths does not exist |
| `link-feasibility` | `same_volume` | `ok` | library and links on the same volume |
| `link-feasibility` | `volumes_differ_symlink_only` | `warn` | two volumes: only symlinks cross |
| `link-feasibility` | `volumes_differ` | `fail` | library and links under two mounts: a single common mount ([05-docker-and-volumes.md](05-docker-and-volumes.md)) |
| `link-feasibility` | `volume_identity_unknown` | `warn` | remote volume without `statvfs`: filesystem identity unknown, hardlink not proven; enable the extension or choose `symlink` |
| `link-feasibility` | `volume_alias_risk` | `warn`, `ok` | `warn`: an sftp provider points at this machine (`localhost`, `127.0.0.0/8`, `::1`) while local library roots exist; the local path and the volume can name the same disk without being matched: name the library and the links through one access only. `ok`: providers of the same address share their library roots |
| `link-feasibility` | `mount_root_undetected` | `warn` | kernel without `STATX_ATTR_MOUNT_ROOT` (Linux before 5.8) and a local link directory: a bind mount of the same filesystem placed under it is not detected; never mount the library under a link directory |
| `fs-providers` | `no_fs_provider` | `skipped` | no provider configured |
| `fs-providers` | `fs_providers_build_failed` | `fail` | `fsProviders` in the database |
| `fs-providers` | `mount_ok` | `ok` | mount described |
| `fs-providers` | `mount_advice` | `warn` | the provider's advice (`advice.mount.*` keys of `hashsource`, rendered in the language): start rclone with `--rc`, backend without MD5 or without date (the comparison rereads the files) |
| `sftp:<name>` | `sftp_ok` | `ok` | the server answers and serves POSIX paths; address and user are shown, never a secret |
| `sftp:<name>` | `host_key_unknown`, `host_key_mismatch`, `known_hosts_unreadable` | `fail` | host key missing, different, or `knownHosts` unreadable: see "By SFTP connection code" |
| `sftp:<name>` | `auth_refused`, `identity_key_too_weak`, `identity_file_unusable` | `fail` | authentication methods refused, RSA key under 3072 bits, unreadable key or wrong passphrase |
| `sftp:<name>` | `identity_file_permissions`, `agent_unavailable` | `warn` | `identityFile` readable by others (`chmod 600`), SSH agent missing: the connection goes on |
| `sftp:<name>` | `remote_windows_unsupported` | `fail` | `RealPath("/")` or a configured folder returns `/C:/…`: Windows server not supported (a Unix folder named `/C` is not one) |
| `sftp:<name>` | `remote_unavailable`, `hostile_response` | `fail` | server unreachable or cut (network, `timeout`), or an impossible value from the server; no server text is repeated |
| `sftp-library:<name>` | `root_ok` | `ok` | remote root readable, media found |
| `sftp-library:<name>` | `library_root_unreadable` | `fail` | root missing, not a folder, or unreadable for the SFTP account |
| `sftp-library:<name>` | `no_media_in_first_2000` | `warn` | no media among the first 2000 entries listed (never more): empty root or wrong mount on the server |
| `sftp-library:<name>` | `library_maybe_writable` | `warn` | mode (group or others write bit) or owner of the library folder: the account looks able to write. Never inferred from a write attempt. Use an SFTP account that is read-only on the library |
| `sftp-library:<name>` | `no_library_root` | `skipped` | no library root on this provider |
| `sftp-exec:<name>` | `exec_ok` | `ok` | remote commands available; shows the hashes (`sha1`, `md5`), whether `nice` and `ionice` are present and, without a link folder on the provider, that capabilities are not probed (`not_probed`) |
| `sftp-exec:<name>` | `exec_off` | `ok` | `exec: off`: no command is sent |
| `sftp-exec:<name>` | `remote_exec_unavailable` | `warn` | `exec: auto` but the probe fails (reason shown: `exec_denied`, `login_shell_not_posix`, `exec_probe_failed`, `exec_inconsistent`): SFTP-only reads |
| `sftp-exec:<name>` | `remote_exec_required_failed` | `fail` | `exec: required` and the probe fails: every write on the volume is refused (`remote_exec_required`) |
| `sftp-exec:<name>` | `capability_probe_failed` | `warn` | the capability probe in the link folder failed |
| `cross-seed` | `cross_seed_unset` | `skipped` | cross-seed not configured |
| `cross-seed` | `cross_seed_mode_invalid` | `fail` | unknown `crossSeedMode` |
| `cross-seed` | `cross_seed_warning` | `warn` | daemon unreachable: `native` fallback in `auto` mode, disabled in `delegate` |
| `cross-seed` | `blocked_environment` | `warn` | `config.js` reads this tool's variables, which are hidden from it |
| `path-mappings` | `no_path_mapping` | `skipped` | no rule and no remote link folder |
| `path-mappings` | `client_path_unmapped` | `fail`, `warn` | only with a writable torrent client: `fail` for a remote link folder, `warn` for a remote torrent root (only dedupe and repair rechecks need it), with no `pathMappings` rule whose `from` is an `sftp://` address covering it and whose `to` is the client's view |
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
| `torrent_not_found`, `torrent_fetch_failed`, `torrent_too_large` | Verify | provider: 404, outage, `.torrent` over 8 MiB; after the fallback, no indexer of the tracker has this `.torrent` (see `searched_sources`, and each indexer's `tracker`) |
| `indexer_unavailable`, `indexer_access_denied`, `indexer_hostile_response` | Verify, Match | indexer down, rate limited (`Retry-After`), API key refused, or hostile response: the indexer is set aside for the rest of the run; in Verify the candidate stays `matched` and resumes later, in Match the remaining groups resume on the next run; `doctor` (`torznab:<name>`) |
| `torrent_fetch_failed` | Verify | fallback: unreadable indexer answer to a search (HTML page, wrong type, oversized body); same effect as the previous row |
| `magnet_only`, `foreign_download_host` | Verify, Match | indexer items ignored: magnet only, or download link outside the indexer's host |
| `torznab_no_exact_size` | Match | indexer `.torrent` without a file of the exact size of a local file (a matching total size is not enough): dropped, never stored; the next result is tried |
| `secret_key_missing` | Match | `NECROMANCIARR_SECRET_KEY` missing: no indexer is queried (counted once) |
| `read_error` | Verify | local read failed: resumes on rerun |
| `no_link_dir_on_same_device` | Link | no `linkDirs` on the source's volume; `linkType` is not `symlink` |
| `link_kind_refused`, `cross_device` | Link | the folder refuses the kind (see `doctor`), or two volumes |
| `destination_conflict` | Link | the torrent folder contains an entry that is not the tool's: nothing is deleted, look at it |
| `link_root_unusable`, `link_dir_overlaps_library` | Link | folder missing, not writable or in the library |
| `library_root_unavailable` | Link | a library root is missing or not mounted: no write root is opened, `doctor` reports it (`library_root_unavailable`) |
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
| `client_path_unmapped` | Seed, `dedupe apply`, `repair apply` | remote link folder with no `pathMappings` rule whose `from` is an `sftp://` location of the same provider: add the rule to the client's view, re-accept then rerun. dedupe and repair refuse before any write when a client recheck is planned |
| `no_atomic_replace` | `dedupe plan` | the torrent folder's volume cannot replace a file atomically (SFTP without `posix-rename`): proposal not applicable, nothing is written |
| `remote_exec_required` | Link, `dedupe plan` | volume set to `exec: required` whose remote command probe failed: write root refused, nothing is written; Link keeps the proposal for the next run, dedupe makes it not applicable. Allow the lines of `volumes commands` (command guard), or set `exec` to `auto` |
| `remote_exec_failed` | dedupe, repair, file identity | a remote command failed after a successful probe (vanished file, timeout, non-conforming output): the item is read again over SFTP (dedupe: full read; repair: start-end fingerprint; identity: unknown). Nothing to do if rare; otherwise see `doctor` |
| `identity_unproven` | Seed | remote link: identity never proven, `skip_checking` refused, the client rechecks and the torrent stays paused until 100% (no action) |
| `remote_unavailable` | Scan, Verify, Link, Seed | the remote volume does not answer: remote library root left as is by Scan, candidate and proposal kept as is (never `stale` nor `failed`, no read counted as different), rerun the stage once the volume is back |
| `symlink_order_unverified` | Link | the SFTP server did not prove the symlink argument order (reversed probe, or no verdict): symbolic links refused on this volume, proposal failed. Choose `hardlink` in `linkKinds`, or fix the server (OpenSSH reads the expected order), then accept again |
| `server_copy_unavailable` | Force | link folder on an SFTP volume without server-side copy (`copy-data`): nothing is copied or changed, the torrent stays paused. Run the tool on the seedbox with a local link folder, or wait for `copy-data` support ([03-seed-safety.md](03-seed-safety.md)) |
| `free_space_unknown` | Force | the free space of the remote link folder is unreadable (server without `statvfs`): nothing is copied or changed; enable the server's `statvfs` extension (only once `copy-data` is supported) |
| `crossseed_notify_failed` | Seed | daemon refuses or unreachable: warning only |
| `no_announce` | Verify, Link, Seed | neither a `trackers` entry nor a sealed indexer `.torrent`: add the `trackers` entry, then accept again (no re-verification) |
| `sealed_torrent_unreadable`, `secret_key_missing` | Seed | sealed `.torrent` unreadable (key changed) or key missing: restore `NECROMANCIARR_SECRET_KEY`, or run Verify again on the candidate |
| `infohash_changed_after_inject` | Seed | the built `.torrent` no longer has the candidate's infohash: nothing is added, run Verify again |
| `indexer_budget_exhausted` | Verify, Match | a request quota for an indexer (per run or per day, sliding 24 h window) is reached: not a failure, the rest resumes on the next run; the daily count survives restarts |

### By SFTP connection code

Stable codes of a connection to an `sftp` provider, identical in both languages.

| Code | Severity | Cause and action |
|---|---|---|
| `host_key_unknown` | refusal | the host has no entry in `knownHosts`: add the key with `sftp trust <name> --yes` or `ssh-keyscan -H seedbox.example.com >> known_hosts` (your host), after checking the fingerprint through another channel (see "Getting the `known_hosts` line" in the configuration) |
| `host_key_mismatch` | refusal | the presented key differs from the pinned one, or it is revoked: the connection may be intercepted. Replace nothing before checking the new fingerprint on the server through another channel; only then replace the line |
| `auth_refused` | refusal | the server refused every configured method (`agent`, `identityFile`, `password`), or none was usable: check `user`, the key authorized on the server (`authorized_keys`), the password variable |
| `known_hosts_unreadable` | refusal | `knownHosts` missing, unreadable or without any valid entry: check the path (as seen by the process or the container), read permissions and the OpenSSH format |
| `known_hosts_not_writable` | refusal | `sftp trust` refuses `knownHosts`: a symlink or a special file (even without `--yes`), or, with `--yes`, a missing folder or a file that is not writable (Docker: folder mounted `:ro`). Nothing is written |
| `no_link_dir` | refusal | `volumes probe`: no `linkDirs` folder on this volume, the probe has nowhere to write; add an `sftp://<name>/…` link folder. Nothing is written |
| `identity_key_too_weak` | refusal | `identityFile` is an RSA key under 3072 bits: generate an ed25519 key (`ssh-keygen -t ed25519`) and authorize it on the server |
| `identity_file_unusable` | refusal | `identityFile` is not a usable private key, or `identityPassphrase` is missing or wrong |
| `remote_windows_unsupported` | refusal | the SFTP server is a Windows server (`RealPath("/")` returns `/C:/…`): not supported, serve the library from a POSIX host |
| `identity_file_permissions` | warning | `identityFile` is readable or writable by group or others: `chmod 600`; the connection goes on |
| `agent_unavailable` | warning | `agent` is requested but `SSH_AUTH_SOCK` is missing or unreachable, or the agent does not answer within `timeout` (never forwarded in Docker): the method is skipped, the others are tried |
| `remote_exec_unavailable` | warning | `exec: auto`, but remote commands are refused, missing, not GNU or inconsistent with SFTP (reason in `remoteExec`): SFTP-only reads (no inode, no remote hash), a single warning per volume and per session |
| `remote_exec_failed` | warning | a command failed after a successful probe; the count says how many items were read again over SFTP |
| `link_dir_world_writable` | warning | folders created in a remote link folder are writable by group or others (one warning per link folder, with their count): set the SFTP account's umask to `022` (for example `ForceCommand internal-sftp -u 022` on OpenSSH) or create these folders beforehand; the operation goes on |

### By SFTP capability reason (`Reasons`)

Reason a feature of the `sftp` volume is absent from its capabilities (links, atomic replace, server-side copy, volume identity).

| Code | Cause and action |
|---|---|
| `not_probed` | no probe in this session: the probe runs in the first link folder opened (Link, `dedupe plan`, `doctor`); without a link folder on the provider it never runs |
| `extension_missing` | the server does not offer the needed SFTP extension (`hardlink`, `posix-rename`, `statvfs`, `copy-data` (OpenSSH extensions)) |
| `probe_failed` | the trial of the feature failed: it is not proven, so it is refused |
| `server_copy_unsupported_by_client` | the server offers `copy-data`, but the SFTP client library never sends that request: Force stays refused (`server_copy_unavailable`) |

### By SFTP volume error

Read or remote command errors of an `sftp` volume that do not show up as a stage reason: the item is read again over SFTP or the stage keeps its state.

| Code | Cause and action |
|---|---|
| `hostile_response` | the server answered an impossible value (size, `statvfs`, link target, oversized listing): refused, never truncated; do not trust the server until it is checked |
| `short_read` | a read ended before the file size (growing file, dropped connection): the file is unavailable for this read (`remote_unavailable`), never judged different |
| `exec_output_too_large` | a remote command output exceeded its bound: session killed, result rejected, item read again over SFTP (`remote_exec_failed`) |
| `exec_path_outside_roots` | a path was under no library, link or torrent root of the volume: nothing was sent |
| `exec_size_mismatch` | the size from remote `stat` differs from the SFTP size: the item is read again over SFTP |

### By remote execution reason (`remoteExec`)

Reason why remote commands are disabled on an `sftp` volume, in the volume capabilities (`remoteExec`); the volume carries on SFTP-only (full reads, no inode and no remote hash), except with `exec: required`.

| Code | Cause and action |
|---|---|
| `exec_off` | `exec` is `off`: no command is ever sent |
| `exec_denied` | the server refuses exec (restricted account, `ForceCommand internal-sftp`, command guard): normal for an SFTP-only account; allow the exact lines of the configuration to benefit from it |
| `login_shell_not_posix` | the login-shell probe finished without returning its exact POSIX output (fish, csh): give the account a POSIX shell (`chsh -s /bin/sh`); no other line is sent for the session |
| `exec_probe_failed` | a probe line did not finish (timeout, oversized output, channel cut): SFTP-only for the stage, probe run again on the next stage; or `stat` missing, failing or not GNU coreutils (BusyBox, BSD): unsupported, SFTP-only |
| `exec_program_missing` | `sha1sum` or `md5sum` missing or not GNU coreutils: remote hashes are unavailable, `stat` is still used |
| `exec_inconsistent` | the commands answer, but `stat` or a hash contradicts SFTP on the probe files (size, date, zero inode, hard link seen as two files, wrong hash), or the server has neither hard link nor atomic replace to create a probe file: a file system with unusable inodes (FUSE, union), SFTP only |

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

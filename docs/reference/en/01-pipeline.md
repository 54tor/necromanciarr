# Pipeline

Five stages run in this order, each through its own command (`scan`, `match`, `verify`, `link`, `seed`) or through `POST /api/stages/{stage}/run`. Each stage reads and writes a state persisted in SQLite (`dbPath`), can be replayed without duplicates and resumes where an interruption left it. Nothing starts by itself: there is no internal scheduling.

## Acceptance rule

**Nothing is applied without an accepted proposal.** Scan, Match and Verify only produce findings and proposals. Link and Seed only process proposals whose decision is `accepted`, from the current generation of a `verified` candidate. There is no automatic mode.

| Stage | Reads | Writes | Required component |
|---|---|---|---|
| Scan | `libraryRoots`, Radarr, Sonarr | inventory (path, size, date, TMDB/TVDB) | `libraryRoots`; `radarr`/`sonarr` for the identifiers |
| Match | inventory, provider | `matched` candidates and their files | `libraryApi` + `LIBRARY_PROVIDER_TOKEN` |
| Verify | candidates, `.torrent`, `.nfo`, local files | verifications, proven associations, annexes, proposals | `libraryApi` |
| Link | accepted proposals with an announce | torrent folder under `linkDirs`, links, annexes, `linked` step | `linkDirs` |
| Seed | `linked` proposals | torrent added to the client with its announce injected in memory, `applied` decision, final step | `torrentClients`; `trackers` or sealed `.torrent` |

A missing component gives `not_configured` (Match, Verify, Seed) or the failure `pipeline: no link directory configured` (Link).

## Scan

1. Walks each root. A symlink at the root is followed, but no symlink inside it is, and a subfolder mounted on another filesystem is ignored. Only media files are inventoried.
2. A file whose size or date changed makes its candidates `stale` (`changed_file_invalidated`). A file missing from disk is removed and makes its candidates `stale` (`missing_file_removed`). If all the files of a root seem absent (detached mount), the root is not pruned (`empty_root_not_pruned`).
3. Each Radarr and Sonarr instance lists its files with their TMDB/TVDB identifiers. Their paths are translated by `pathMappings` (from the `to` side to the `from` side) then matched against the inventoried files. Two instances that disagree: the first value seen wins (`conflicting_ids`). If an instance answers but no path matches while some files already have identifiers, the identifiers are kept (`arr_paths_matched_nothing`): a wrong rule must not erase them.

For each identified file the scan also records the IMDb identifier, the title, the year and, for Sonarr, the season and the first episode of the file (two requests per series, season 0 for specials). Titles are stripped of control and formatting characters, limited to 256 characters, used only for the candidate search and never logged. A title changed on the instance is picked up by the next scan.

A file without an identifier is not searched in v1 (`no_external_id`): the first ten names are returned in `unidentified_samples`. Identifiers only come from Radarr and Sonarr.

## Match

1. Makes `stale` every candidate whose local file changed or disappeared.
2. Groups the files by identifier (TMDB, otherwise TVDB) and queries the provider once per identifier with the sizes of these files (`size` filter, 50 values per request).
3. Accumulates each release found over all the groups where it appears, then keeps it only if one of its files has exactly the size of a local file (`no_exact_size` otherwise). A release is recorded once, identified by (infohash, tracker): the same infohash on two trackers gives two candidates.
4. New candidate: `matched`. Known candidate with identical associations: unchanged. Associations changed, or `stale` candidate: associations replaced, verification erased, open proposals closed, back to `matched` with one more generation.
5. Recomputes the groups.

Provider rate-limited (429) or unavailable: the stage stops cleanly with `RetryAfter`, everything found is already recorded, the rerun resumes. Access denied or context cancelled: stop. A search that fails for another reason is counted (`search_failed`) and the stage continues; after an incomplete run, a known candidate is not re-associated if the accumulated association would lose files (`update_deferred_incomplete_run`).

## Verify

Processes each `matched` or `fetched` candidate one by one.

1. **Unchanged files**: the size and date of each associated file are compared with the association; otherwise `stale` (`changed_file_stale`).
2. **`.torrent`**: taken from the database if already `fetched`, otherwise downloaded (at most 8 MiB kept) and the candidate becomes `fetched`. A v2-only or invalid torrent, or one whose infohash differs from the candidate, makes the candidate `failed` (`v2_only`, `invalid_torrent`, `infohash_mismatch`), as do `torrent_not_found`, `torrent_fetch_failed` and `torrent_too_large`.
   - **Indexer fallback**: on the provider's `torrent_not_found` only (no other error), Verify queries, in `sourceOrder`, the indexers whose `tracker` equals the candidate's (case and spaces ignored); an indexer without `tracker`, or a candidate without a tracker, is never involved. One text search per indexer (`q=` release name, control characters removed, at most 256 characters), then at most 3 `.torrent` downloads per candidate across all indexers; an item announcing another infohash is not downloaded. Only a `.torrent` whose **computed** infohash equals the candidate's is kept, the first one found: the database keeps its copy without announce in `torrent`, the full copy sealed in `torrent_sealed`, and `source` becomes `torznab:<name>`. Items announcing the candidate's infohash are tried first, then items without the attribute in feed order; once the 3 downloads are spent, the next indexers are not queried. If a `trackers` entry covers the candidate's tracker, nothing is sealed (the announce will come from the configuration) and `NECROMANCIARR_SECRET_KEY` is not required for this fallback; otherwise, without it, no indexer is queried (`secret_key_missing`, candidate `failed`). The stage result gives `searched_sources` (ids of the queried sources, per candidate); refused items are counted (`magnet_only`, `v2_only`, `invalid_torrent`, `infohash_mismatch`, `foreign_download_host`, `torrent_too_large`) only for a candidate still not found, then `failed` (`torrent_not_found`). An indexer left on the way leaves the candidate `matched`, resumed on the next run. Budget exhausted (`indexer_budget_exhausted`): no further request to this indexer in the run. Outage without `Retry-After` or timeout (`indexer_unavailable`), access denied (`indexer_access_denied`), hostile (`indexer_hostile_response`) or unreadable (`torrent_fetch_failed`: HTML page, wrong type, oversized body) answer to a search: the indexer is set aside for the rest of the run, and every later candidate of its tracker gets the same reason with no new request. A `Retry-After` sets it aside until it ends (one hour at most), beyond the run. A `.torrent` refused at download (v2 only, invalid) only sets that item aside.
3. **Association by hash**: the local file and the torrent file are first paired by size. If several local files have the same size, the pieces are hashed to tell them apart, within `tieBreakReadBytes` bytes per candidate (50 GiB by default): beyond that, the reason `read_budget_exceeded` is counted, the files not told apart stay ambiguous (a `conflict`) and the files already proven continue verification. With no paired file, the candidate is `failed` (`no_file_matched`). A file that is still ambiguous stays ambiguous: it is never picked.
4. **Missing `.nfo` files**: the torrent's `.nfo` files absent from the library are requested from the provider. Each one is tried under four byte variants (raw, without BOM, LF line endings, CRLF), of the same size as the torrent file. A variant is only kept if all the pieces covering the file agree, which requires all the other files of these pieces to be available. It is then stored in the database (at most 1 MiB) as a verified annex. Otherwise the `.nfo` stays missing.
5. **Coverage**: each piece whose files are all available is hashed with SHA1 and compared with the `.torrent`. The bytes of a piece straddling an absent file are never counted as owned. Reading goes through the `readBytesPerSecond` limiter; progress is recorded every 64 pieces or 5 seconds.
6. **Verdict**: see the table of kinds. A present media file with a piece whose SHA1 is wrong makes the candidate `failed` (`media_hash_failed`) without a proposal; a present annex in that case is excluded from the association and the link (`annex_hash_failed`) and the client will download it. The files are verified again at the end: if they changed during reading, the candidate is `stale`.

A failed local read (`read_error`) does not change the candidate's state: its recorded verification serves as the resume point on rerun.

## Proposal kinds

| Kind | Created by | Meaning | Possible decision |
|---|---|---|---|
| `full` | Verify | All pieces verified, no missing media | `accept`, `reject`, `choose` |
| `partial_annex` | Verify | All media present and verified; some annex pieces missing | `accept`, `reject`, then `force` after seed |
| `partial_pack` | Verify | Some of the torrent's media are not in the library | `accept --allow-partial-pack` only; never forceable |
| `conflict` | Verify, groups | Several possible torrents for the same file, or an ambiguous local file | `choose` (never `accept`) |
| `cross_seed` | groups | `native` mode: another complete torrent of the group | `accept`, or `accept-group` |
| `dedupe` | `dedupe plan` | Replace a torrent-side copy with a link | `dedupe apply` |
| `repair` | `repair scan` | Remake a broken link to its new target | `repair apply` |

Verify creates no proposal when the media are not all covered without it being a partial pack (`media_not_verified`): the candidate stays `verified` with no proposal.

### Cap on missing pieces for partials

`partialMaxMissingPercent` (default `100`, no cap) bounds the share of missing pieces (missing pieces / torrent pieces) of a `partial_annex` or `partial_pack`. Equal to the cap is still proposed; above it, the candidate stays `verified`, no proposal is created and its verdict is set aside with the reason `missing_pieces_above_cap`: counted in Verify's result (`reasons`) and listed by `status` (field `held`: kind, missing pieces, reason). `full` torrents and conflicts are never capped.

Each Verify applies the current cap without reading the files again: a set-aside verdict that is back under the cap becomes a `pending` proposal; a partial proposal still `pending` that is now above it becomes `superseded` and its verdict is set aside. A proposal that is already accepted, applied, rejected or failed is never withdrawn. A set-aside verdict only holds for the candidate's generation: a new verification replaces it.

### Decisions

```
pending  -> accepted | rejected | superseded
accepted -> applied  | failed   | superseded
rejected -> superseded
failed   -> accepted (réacceptation)
```

- `accept` on a `failed` proposal puts it back to pending then accepts it; on a proposal that is already `accepted` or `applied`, it does nothing.
- `reject` is only possible from `pending`. An accepted proposal cannot be taken back in v1.
- `applied` is set by Seed (step `seeded`, `seeded_paused_incomplete`) or by the dedupe/repair action. The `linked` step leaves the decision at `accepted`.
- `superseded` closes a proposal whose candidate became stale or re-associated. It never comes back.
- At most one non-closed proposal per candidate, kind and generation. A decided proposal (`rejected`, `applied`) from an earlier generation does not block the new one.
- Application steps (`step`): `linked`, `seeded`, `seeded_paused_incomplete`, `forced`.

## Generations and staleness

A candidate's generation increases on every invalidation. A proposal carries the generation of its creation; `current` is true only if it equals that of a `verified` candidate. A stale proposal cannot be accepted, linked, seeded or forced: the action is refused with `stale_proposal` (exit 3, HTTP 409) and a new verification is needed.

Causes of staleness: local file modified, disappeared or replaced (Scan, Match, Verify), different associations (Match), source modified detected by Link, Seed or Force. The candidate becomes `stale`, its `pending`, `accepted` and `failed` proposals become `superseded`, its verification and proven association are erased. A `failed` candidate only goes back to `matched` if its associations change.

## Groups

Candidates competing for the same local file form a group (`group_id`): a single episode and a season pack, the same release on two trackers. The group is recomputed by Match and Verify.

- **Primary**: the open `full` proposal with the highest verified percentage; on a tie, the smallest candidate. Only a `full` can be primary. `proposals choose <id>` sets it by hand and the automatic election no longer replaces it as long as it stays open.
- **Conflict**: as soon as two members of the group have a proposal, a `conflict` proposal lists all the torrents of the group without choosing one. Accepting it is refused (`refused`): a torrent is chosen.
- **One torrent per file**: when cross-seed's effective behaviour is `disabled` (mode `off`, or `delegate` without a reachable daemon), only one torrent proposal per group is accepted or applied (`cross-seed is disabled, another torrent of this group is already accepted`). In all modes, a single candidate never has two accepted torrent proposals.
- **`accept-group`**: accepts the primary. In `native` mode, it also accepts, for each other verified torrent, its `cross_seed` proposal (or else its `full`).
- An open proposal attached to a different group than its candidate blocks Link and Seed (`refused` refusal, `proposal group differs from its candidate group`) until Match or Verify has resynchronized.

## `.torrent` storage

| `torrent_candidates` column | Content | Cleared |
|---|---|---|
| `source` | `provider`, or `torznab:<name>` for an indexer: the origin of the stored `.torrent`, which can differ from the source seen at Match (Verify fallback), because the seal is bound to it. Otherwise the first source that supplied the candidate, kept afterwards. Candidates of an earlier database read `provider` | never |
| `torrent` | `.torrent` without announce: Verify keeps only the sanitized copy (`info` copied byte for byte, so the same infohash), for the provider as for an indexer; a `.torrent` that sanitizing refuses makes the candidate `failed` (`invalid_torrent`) without stopping the stage. Column backstop: a top level carrying `announce`, `announce-list`, `url-list`, `httpseeds` or `comment`, or that cannot be read, is refused (`ErrUnsanitizedTorrent`); a file named `announce` inside `info` is accepted. At most 8 MiB | with the candidate |
| `torrent_sealed` | an indexer's complete `.torrent` (it carries the passkey), sealed with `NECROMANCIARR_SECRET_KEY` and bound to the row's `infohash`, `tracker` and `source`; at most 8 MiB + 128 bytes, enforced by the database | see below |

The database clears the sealed blob in the very statement that writes one of these changes, with no window where one goes without the other:

- the candidate becomes `failed` or `stale`, with or without proposals (a retry downloads the `.torrent` again);
- a proposal of the candidate becomes `applied`, `rejected` or `superseded` and the candidate has no other `pending`, `accepted` or `failed` proposal left apart from `conflict` (a conflict is never seeded): a rejected conflict does not clear it while the same candidate's `full` stays open;
- a tracker that a `trackers` entry now covers (tracker compared byte for byte with the stored value): at service startup and at every read of the proposals.

A proposal whose candidate has neither a `trackers` entry nor a sealed blob is `no_announce`: Verify counts it under this reason, every read marks it `seedable: false`, and Link refuses it before creating anything, like a proposal whose sealed `.torrent` has no `NECROMANCIARR_SECRET_KEY` (`secret_key_missing`) ([03-seed-safety.md](03-seed-safety.md)). Restart the service after editing `trackers`.

A `pending` or `failed` proposal that is never decided keeps the blob, since Seed needs it; so that nothing stays forever, every Verify first clears any blob whose candidate has not been updated for 30 days.

## Resumption, lock, caps

- **Stage lock**: only one stage, or a `dedupe plan/apply` or `repair scan/apply` action, runs at a time, whatever the process (CLI, server). The lease lasts 2 minutes and is extended every 40 seconds. A second launch fails with `stage_running` (CLI exit 1, HTTP 409). A dead process frees the slot after the lease. If the lease can no longer be extended (taken over by another, 3 failures in a row), the stage stops with `pipeline: run lock lost` and writes nothing more.
- **Interruption**: Ctrl-C or SIGTERM cancels the current action and the consistent state is recorded; a second Ctrl-C kills the process. Rerunning the stage resumes.
- **Read rate**: `readBytesPerSecond` (20 MiB/s by default) bounds the reads of Verify, deduplication and link fingerprints, burst 1 MiB. `tieBreakReadBytes` (50 GiB) only bounds the tie-breaking of same-size copies; a candidate's coverage has no byte cap.
- **Provider**: 4 simultaneous requests, 30 s per request, JSON responses of at most 16 MiB and `.torrent` files of at most 32 MiB, cache of 1 h for successes and 10 min for 404s.
- **Client recheck**: one hour at most; beyond that the torrent stays paused.
- **Link inspection** (`links list`): 20 s; only one inspection at a time.
- **Diagnostics**: 10 s per check.

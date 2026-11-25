# Seed safety

Seeding false content penalizes the ratio with the trackers, and a client that writes to a linked file modifies the library. These invariants are enforced by the code of Link and Seed, not by convention. Each refusal reason below is the reason counted in the stage result (`reasons`) and added to the proposal as the warning `application failed: <raison>`; for `cross_device`, `no_link_dir_on_same_device` and `link_kind_refused`, the warning adds in parentheses the cause and the setting to change (no fallback to another link kind is attempted).

## The invariants

| # | Invariant | Where |
|---|---|---|
| 1 | A torrent is **always added paused** (`Start: false`). | Seed |
| 2 | A torrent is **resumed only at 100%**, and only if Necromanciarr's verification covered all the pieces (kind `full` or `cross_seed`). | Seed, `RecheckAndGuard` |
| 3 | `skip_checking` is only requested if verification is complete, `skipRecheck` is `true` and **every link is proven by file identity**; a proof by fingerprint alone (reflink, copy) forces a client recheck. A remote link (`sftp://`) is **never** proven, even when the volume returns a file identity (`identity_unproven`). | Seed |
| 4 | Sources, links and annexes are **verified again just before the add**. | Seed |
| 5 | A `partial_annex` stays **paused** until an explicit Force. A `partial_pack` is **never forceable**. | Seed, Force |
| 6 | A torrent **already present in the client** is only taken back if it points to our folder; otherwise it is refused and left intact. | Seed |
| 7 | **No write to the library.** | everywhere |
| 8 | No application without an accepted proposal of the current generation. | Service |

### 1 and 2: added paused, resumed at 100%

An added torrent downloads nothing while it is paused. It is only resumed in two cases:

- `skip_checking` accepted (invariant 3): the client shows 100% by construction, which only proves the add. Seed only resumes if progress is 1; the proof of content is the re-verification of invariant 4.
- Otherwise, `RecheckAndGuard`: pause, recheck by the client, then resume **only** if the final state is complete at 100%. Incomplete torrent, timeout (1 hour), cancelled context or error: the torrent is paused again (even if the client had restarted it), the proposal becomes `applied` at the step `seeded_paused_incomplete` with the warning `left paused: the client did not reach 100% after its recheck`. A failed pause or recheck is a refusal (`pause_failed`, `recheck_failed`, `resume_failed`).

After a successful resume, the step is `seeded`. In `delegate` mode, cross-seed is then notified.

### 3: `skipRecheck`

`skipRecheck` (default `true`, importable from cross-seed) never overrides the principle of clean verification: it only applies to a torrent whose pieces are all verified by us. Why verify again before the add: with `skip_checking`, libtorrent checks lazily; a source modified in the meantime would make the client download into the linked inode.

A remote link folder (`linkDirs` as `sftp://<provider>/<absolute path>`) never gets `skip_checking`: SFTP returns no inode, and even an identity returned by the server does not prove that the client reads that file. The link is recorded with its volume (`volume` column of `links`), so it stays unproven from one run to the next; with `skipRecheck` and a complete verification, Seed counts `identity_unproven`, adds the torrent paused without `skip_checking`, and `RecheckAndGuard` resumes it only at 100%. Force keeps the same rules (`partial_annex` paused, `partial_pack` never); on a remote volume it refuses the isolation (see "Force on an SFTP volume" below).

### 4: verification before the add

Just before the add, Seed:

1. reloads the proven association and compares size and date (to the nanosecond) of each source file; a changed source makes the candidate `stale`, nothing is added, warning `not seeded: a library file changed since it was linked, verify again`;
2. checks each link of the torrent folder (same file as its source, or same size and same start-end fingerprint for a reflink) and each annex (size and SHA-256); damage deletes the step and makes the proposal fail (`link_changed`, `annex_changed`): re-accepting then rerunning Link rebuilds the folder;
3. refuses an invalid client path (`client_path_invalid`, for example a character forbidden by Windows in a drive path); for a remote link folder, requires a `pathMappings` rule whose `from` is an `sftp://` location of the same provider, otherwise refuses (`client_path_unmapped`): "same path" makes no sense between two machines;
4. a remote volume that does not answer (`remote_unavailable`) leaves the proposal linked, with no failure and no step cleared: the rerun picks it up. Link does the same: the proposal stays accepted. So does Verify: the candidate is never marked `stale` because its files could not be read.

### 5: partial_annex and Force

A `partial_annex` is added paused, never checked with `skip_checking`, and becomes `applied` with the step `seeded_paused_incomplete` (`paused_incomplete`). Right after the add, Seed requests the pause again then rereads the torrent by its hash (up to 10 attempts 200 ms apart, the add being asynchronous): if it does not appear, or in a state other than stopped or checking, Seed requests the pause again and rereads; if still running, the torrent is removed from the client **without deleting its files** (`deleteFiles=false`, they are links) and the proposal fails with `not_paused_after_add`. If not found, it fails with the same reason. The missing pieces cover annexes absent from the library, often straddling the end of a linked media file. The client downloads and writes each missing piece **in full**, including the part that belongs to the media file: through a hard or symbolic link, it would write unverified blocks into the library file.

`Force` is the only way to start this torrent:

| | CLI | Web |
|---|---|---|
| Action | `proposals force <id> --yes` | `POST /api/proposals/{id}/force` with `{"confirm": true}` |
| Without confirmation | warning on stderr, nothing is started, exit 2 (`confirmation_required`) | 400 `confirmation_required` |

Conditions: kind `partial_annex`, decision `applied`, step `seeded_paused_incomplete`, current generation, client configured, sources unchanged (otherwise `stale` candidate and `stale_proposal` refusal). A `partial_pack` returns `a partial pack can never be forced`.

Before resuming the torrent, Force isolates the exposed media files:

1. it computes the missing pieces: those that cover a file that is neither linked nor a verified annex;
2. each linked media file that covers one of these pieces and still points to the library file (hard link, or symbolic link) is replaced by an independent copy, written in the torrent's link folder: reflink if the filesystem allows it (copy on write, no shared block can be modified), otherwise a full copy. A reflink or a copy already in place is not touched, nor is a media file that shares no missing piece;
3. a full copy requires on the link folder's volume its size plus a margin (64 MiB plus 1% of the size). All the copies are prepared in temporary files before the first replacement, then put in place by renaming within the same folder; the link is recorded again with its new kind (`reflink` or `copy`);
4. the folder is verified again (links, copies, annexes), then the client is resumed and the step becomes `forced`. The response lists the replaced files (`replaced`).

If a media file can be neither cloned nor copied (copy refused, insufficient space, free space unreadable on the platform), Force refuses with `force refused, a straddling media file cannot be copied out of the library` (code `refused`): no copy remains, no link is replaced, the torrent stays paused. A folder damaged in the meantime gives `the torrent link folder no longer matches its proven content`. The library is never written, neither by the tool nor by the client. Force holds the stage lock: a second Force, or any stage, dedupe or repair, waits for it to finish and fails until then with `stage_running`, so that two copies never count on the same free space.

Force on an SFTP volume: when an exposed media file would have to be copied into a remote link folder, Force is refused before any write with `server_copy_unavailable` ("Force is not available on seedbox: the server offers no server-side copy (copy-data)", code `refused`): the SFTP client does not send `copy-data`, and a stream copy would carry the whole media file through this host, twice across the network, and could leave a half-copied file. No copy is attempted, nothing is written, the proposal and its step do not change. Unreadable free space (a server without `statvfs`) gives `free_space_unknown`. What to do: run Necromanciarr on the seedbox itself, with a link folder local to the server, or keep this torrent paused until `copy-data` is supported. A Force with no media file to copy (no exposed link) is not affected.

Accepting a `partial_pack` already requires an explicit act (`--allow-partial-pack`, or `{"allow_partial_pack": true}`); it is added paused like a `partial_annex` and can never be started by Necromanciarr.

### 6: foreign torrents

Before adding, Seed lists the client's torrents. If the infohash exists:

- its save path equals ours (cleaned comparison, case ignored for a Windows path): the torrent is taken back. If incomplete, it is paused; if complete, it goes through the guarded recheck. This is the recovery after an interruption between the add and the recording.
- otherwise: `already_in_client` refusal, no action on that torrent.

A torrent that is not ours is never paused, deleted or moved. A single candidate is only linked once (`candidate_already_linked`, `already_linked`).

### 7: the library is never written

- Library files are opened read-only (`O_RDONLY`).
- Links, annexes and probes are only created under `linkDirs` or `dedupe.torrentRoots`, which never overlap a library root: refused at load time, at the Link stage (resolved paths) and by `doctor`, which runs no probe in a library root.
- The SQLite database refuses to live in the library (`doctor`, `config-dir`).
- `dedupe` replaces files **on the torrent side**; `repair apply` only touches links under `linkDirs`, `dedupe.torrentRoots` or the scan folder.
- A hardlink shares the inode of the library file: any client writer on that link writes into the file. That is why invariant 5 exists.

On an SFTP volume (`sftp://`):

- The library is only read, through read-only opens (`SSH_FXF_READ`). Every write request (creation, `Setstat`, rename, removal, folder, link) targets a link folder, a torrent root or the folder of a repair scan.
- Three guards enforce it. The registry refuses a write root that overlaps a library root, with paths resolved by the server (`RealPath`). The volume itself refuses, before any request, a folder that overlaps the library roots of its provider. The probe (symlink, hardlink, atomic replace, `statvfs`) runs once per session, in the first write root opened, before a stage gets it; `dedupe plan` opens the torrent root to trigger it when nothing is probed yet. A failed probe refuses the root, and nothing is written. Assumption: the result holds for the whole volume during the session (one server offers the same functions in all its folders).
- A hardlink keeps its source in the library: only its link count and ctime change, as on a local disk. A symbolic link (a `symlink` link, a repaired link) names a library file without writing it.
- Proof: an end-to-end test runs scan, Match, Verify, Link, Seed, Force, dedupe (dry run, then real), repair and `doctor` through an in-process SFTP server. It requires zero write requests in the server log under the library root as the server resolves it, and an identical library tree (content, mode, date, inode). Each stage must prove it did work, and a malicious writer planted in the test is seen by both checks.
- Accepted risk: SFTP has no `openat`. Between the check of a parent folder and the write, someone who already writes into the remote link folder can replace that parent with a link to the library. Recommended defence: an SFTP account with no write access to the library (ACL, group, read-only mount on the server).

## Passkeys of indexer torrents

A `.torrent` downloaded from a Torznab indexer carries the tracker announce, hence its passkey. It is never kept in clear. Match does not download indexer `.torrent` files yet: what follows is the format in place for when it does.

- **Sanitized copy** (in clear): the `info` dictionary copied byte for byte (never decoded then re-encoded: the infohash does not change, even for a non-canonical `info`), plus `creation date`, `encoding` and `piece layers` (hashes only). Everything else at the top level is removed: `announce`, `announce-list`, `comment`, `created by`, `url-list`, `httpseeds`, `nodes`, `publisher-url`, `azureus_properties` and any unknown key. A `.torrent` above 8 MiB, nested deeper than 64 levels, unreadable or with a top-level key appearing twice is refused, and the error never quotes its content.
- **Sealed copy**: the full `.torrent` encrypted with AES-256-GCM under `NECROMANCIARR_SECRET_KEY`. A blob is version (1 byte, value 1) ‖ nonce (12 bytes) ‖ ciphertext ‖ tag (16 bytes); the nonce is drawn from `crypto/rand` for every seal, and a failing random source makes the seal fail. The associated data is the version byte followed by `infohash|tracker|source` (infohash as 40 lowercase hex characters, tracker possibly empty, source `provider` or `torznab:<name>`); `|`, control characters and invalid UTF-8 are refused in every component, so two distinct bindings never collide; the version byte precedes this associated data, so a changed version fails authentication. On unsealing, the binding is always rebuilt from the candidate row (infohash, tracker and source as stored when sealing), never from the current configuration, and the tracker is compared byte for byte. A blob moved to another candidate, tracker or source, truncated, altered or opened with another key gives one single error, `sealed_torrent_unreadable`, which does not tell which of these causes.
- **The key**: 32 bytes in base64url without padding (43 characters), drawn by `necromanciarr api-key --secret`, which prints it on stdout only. Only this spelling is accepted (no standard base64, no hex, no spaces); an invalid value is a usage error that names the variable only. The key is never serialized and is masked everywhere in its likely spellings (raw, base64, hex). Zeroing it in memory is best effort only: Go does not guarantee there is no copy.
- **Key lost or changed**: candidates stay readable (the sanitized copy is enough for Verify), but Seed refuses a candidate whose blob no longer unseals and `doctor` (`secret-key`) reports it as `sealed_torrent_unreadable`. Running Verify again on this candidate fetches a fresh `.torrent` within the indexer budget. Back the key up with the `.env`.

## Announce injection

The provider's `.torrent` has no announce, and the kept copy of an indexer `.torrent` is stripped of it. Seed hands the client a `.torrent` built in memory: the sanitized copy from the `torrent` column (`info` copied byte for byte, `private` flag intact), plus `announce` and `announce-list`.

| Source (`announce_source`) | Condition | Injected announce |
|---|---|---|
| `config` | a `trackers` entry covers the candidate's tracker (key or alias, after normalization) | the configured one, which replaces any other, even an indexer's |
| `indexer` | no entry, but a sealed `.torrent` with the same infohash is stored | the sealed `.torrent`'s one, unsealed in memory with the binding of the candidate row |
| `none` | neither (an empty tracker included: only the `indexer` path remains) | none: `no_announce` |

- **`no_announce`**: the proposal carries the warning "no announce for tracker <name>: add a `trackers` entry" and `seedable: false`. Both are computed at every read (`proposals list`, `status`, Link, Seed) and never stored: adding the `trackers` entry lifts the state without re-verifying. Accepting stays allowed; Link refuses the proposal (`no_announce`) before any probe or folder creation, and Seed refuses it again as a defence. A public torrent follows the same rule: no DHT-only seed. The interface shows the "No announce" pill, whose tooltip says that linking will be refused until a `trackers` entry is added; the Accept button stays enabled, as in the CLI. An empty tracker is named "(unknown)".
- **Checks before the add**: a sealed `.torrent` that no longer unseals gives `sealed_torrent_unreadable` (run Verify again), a sealed `.torrent` without `NECROMANCIARR_SECRET_KEY` gives `secret_key_missing`, and an infohash read back (after unsealing, then after injection) that differs from the candidate's gives `infohash_changed_after_inject`. In all these cases, nothing reaches the client; `secret_key_missing` is already refused by Link, before any probe or folder creation.
- **Memory only**: the injected `.torrent` is passed only to the client add; it is never written to disk, to the database, to a stage result, a warning, a response or a log, and its buffer is cleared after the add (best effort: Go may have made copies). The unsealed plaintext is cleared as soon as its announce is read; the passkey values it carries (path segments, parameter values) mask any error leaving the stage. The client's add error is never read back: a server that copies the received body into its response leaks nothing.
- **cross-seed**: in `delegate` mode, the notification sends only `{"infoHash": …}`. In `native` mode, each torrent of a group resolves its own announce and can be `no_announce` alone; `accept-group` accepts anyway, Link and Seed handle each proposal by its own announce. qBittorrent then keeps the announce in its own state, outside Necromanciarr's control.

| Data | Where | Lifetime |
|---|---|---|
| Configured announce | template in `config.json`, value in the environment | — |
| Complete indexer `.torrent` | `torrent_sealed`, sealed | until the decision, or as soon as a `trackers` entry covers the tracker (at startup and at every read) |
| Injected `.torrent` | nowhere | the time of the add |

After editing `trackers`, restart the service: a daemon started before the edit keeps the old section, while a command run with the new one already clears the sealed `.torrent` files the entry covers; the daemon would then see a transient `no_announce`. On reads, this clearing is best effort: a busy database postpones it (log line `sealed_clear_failed`) without depriving the operator of the proposals; at startup, a failure stops the opening.

## Passkey masking

Every output (CLI text and `--json`, web API, interface, `doctor`, server log, job results, panic messages) goes through the same masking:

| Rule | Masked example | Rendered |
|---|---|---|
| Known secret (value interpolated in `trackers` of at least 8 characters, `torznab` and `prowlarr` keys, `NECROMANCIARR_SECRET_KEY`…), in every form | a bare passkey in a message | `***` |
| Announce URL rendered from the configuration | `https://u:p@tracker-a.example.com:8443/abc/announce?pk=x` | `https://tracker-a.example.com:8443/***` |
| Parameter with a passkey name in a URL, whatever its length (`passkey`, `pk`, `authkey`, `torrent_pass`, `key`, `uid`, `apikey`, `secret`, `token`, case and encoding ignored, separator `&` or `;`, fragment included) | `?pk=x` | `?pk=***` |
| Path segment following such a name | `/passkey/x/announce` | `/passkey/***/announce` |
| Segment or value that contains, once decoded (up to four times: `%252F`), a hex or base64url word of 16 characters or more | `/0000aaaa0000aaaa0000aaaa0000aaaa/announce` | `/***/announce` |
| URL credentials | `https://alice:x@tracker-a.example.com/` | `https://***@tracker-a.example.com/` |
| `trackers` value shorter than 8 characters: segment or value exactly equal, whatever the parameter name | `/42/announce?id=42` | `/***/announce?id=***` |
| Word of at least 16 characters with a digit in the host (token as a subdomain) | `https://0000aaaa0000aaaa0000aaaa0000aaaa.t.example/` | `https://***.t.example/` |
| JSON-escaped URL (`https:\/\/…`, PHP `json_encode` body) | an announce URL in a response body | `***` |

Accepted limits: a passkey shorter than 16 characters in a path, neither after a passkey name nor a `trackers` value, stays visible; a standard base64 passkey (`+`, `/`) is cut into pieces shorter than 16 characters and is masked only if it is a known secret; a URL without `scheme://` is not recognized; a URL split by a space or a newline is handled only up to the split. Outside a URL, a bare passkey is masked only if it is a known secret (8 characters at least); when Seed reads the announce of an indexer `.torrent`, its passkey joins the known secrets for the time of the stage: only values enter (8 bytes at least for replacement everywhere, shorter ones kept for URLs), never ordinary path words such as `announce`. An infohash placed in a URL is masked by the 16-character rule (harmless: it is not secret); alone, outside a URL, it stays visible. Masking runs in linear time and a second pass changes nothing.

## Resumption after an interruption

| Moment of the interruption | Effect on rerun |
|---|---|
| During Link, folder half filled | The entries present are compared with the source: identical content adopted and recorded (a reflink included); recorded but different entry removed; unknown entry: `destination_conflict` refusal and nothing is deleted. Stale entries are only removed if the folder contains only tool entries. A failure undoes what the run had created (links, annexes, folder). |
| During the writing of an annex | Written under a temporary name `.necromanciarr-tmp-*` then renamed; a leftover temporary file is recognized as a tool entry and removed with the folder's stale entries |
| Between the add to the client and `applied` | Seed finds the torrent at our path and takes it back (invariant 6) |
| During `dedupe apply` | The link is created under `<file>.necromanciarr-tmp-<size>-<random>` then renamed over; `dedupe plan` finishes the rename if the torrent file is missing and the entry has the right size, otherwise removes the temporary entry |
| During Verify | Progress recorded (64 pieces or 5 s): the rerun skips the pieces already verified if their files are still present |
| Stage lock not released | Freed at most 2 minutes after the process dies |

If a source changes between Link and Seed, the links stay on disk (they do no harm) but nothing is added to the client.

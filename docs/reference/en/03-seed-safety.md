# Seed safety

Seeding false content penalizes the ratio with the trackers, and a client that writes to a linked file modifies the library. These invariants are enforced by the code of Link and Seed, not by convention. Each refusal reason below is the reason counted in the stage result (`reasons`) and added to the proposal as the warning `application failed: <raison>`; for `cross_device`, `no_link_dir_on_same_device` and `link_kind_refused`, the warning adds in parentheses the cause and the setting to change (no fallback to another link kind is attempted).

## The invariants

| # | Invariant | Where |
|---|---|---|
| 1 | A torrent is **always added paused** (`Start: false`). | Seed |
| 2 | A torrent is **resumed only at 100%**, and only if Necromanciarr's verification covered all the pieces (kind `full` or `cross_seed`). | Seed, `RecheckAndGuard` |
| 3 | `skip_checking` is only requested if verification is complete, `skipRecheck` is `true` and **every link is proven by file identity**; a proof by fingerprint alone (reflink, copy) forces a client recheck. | Seed |
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

### 4: verification before the add

Just before the add, Seed:

1. reloads the proven association and compares size and date (to the nanosecond) of each source file; a changed source makes the candidate `stale`, nothing is added, warning `not seeded: a library file changed since it was linked, verify again`;
2. checks each link of the torrent folder (same file as its source, or same size and same start-end fingerprint for a reflink) and each annex (size and SHA-256); damage deletes the step and makes the proposal fail (`link_changed`, `annex_changed`): re-accepting then rerunning Link rebuilds the folder;
3. refuses an invalid client path (`client_path_invalid`, for example a character forbidden by Windows in a drive path).

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

# Necromanciarr reference

For media files already present in a library (Plex, Radarr, Sonarr), Necromanciarr finds the torrents they came from. It verifies the content piece by piece, rebuilds the torrent's folder tree with links to the library, then adds the torrent to qBittorrent without re-downloading. It never modifies a library file: it only creates links next to it.

This reference describes the behaviour of the shipped code. It is kept up to date with it; the site (`docs/site`) is its public presentation.

## Contents

| Page | Content |
|---|---|
| [01-pipeline.md](01-pipeline.md) | The five stages, states, proposals, generations, verification, resumption |
| [02-configuration.md](02-configuration.md) | `config.json`, `.env`, environment variables, priorities, cross-seed import, diagnostics |
| [03-seed-safety.md](03-seed-safety.md) | The invariants that protect the library and the ratio |
| [04-cli-and-web.md](04-cli-and-web.md) | Commands, exit codes, web server, authentication, routes, interface |
| [05-docker-and-volumes.md](05-docker-and-volumes.md) | Image, volumes, permissions, deployment example |
| [06-limits-and-troubleshooting.md](06-limits-and-troubleshooting.md) | Known limits, risks accepted in v1, troubleshooting |

The library provider contract is in [`docs/contract/openapi.yaml`](../../contract/openapi.yaml).

## Vocabulary

| Term | Meaning |
|---|---|
| Library | Folders (`libraryRoots`) that hold the media. Read, never written. |
| Media file | File with the extension `mkv mp4 avi m4v ts m2ts wmv mpg mpeg mov flac mp3 m4a aac ogg opus`. |
| Library provider | HTTP service that serves the releases, their `.torrent` files and their `.nfo` files (`libraryApi`). |
| Candidate | A provider release with at least one file whose size exactly matches a local file. Identified by (infohash, tracker). |
| Association | Link between a torrent file and a local file, first by size, then proven by hash. |
| Annex | Torrent file that is not media (`.nfo`, sample, subtitle, image). Usually absent from the library. |
| Piece | Block of the torrent checked by a SHA1. A straddling piece covers the end of one file and the start of the next. |
| Proposal | A result the operator must decide on: accept, reject or choose. It has a kind (`kind`) and a decision (`decision`). |
| Generation | A candidate's counter, incremented on every invalidation. A proposal is only valid for the generation it was born in. |
| Stale (`stale`) | State of a candidate whose local file changed, disappeared or whose association changed. |
| Group | Set of candidates competing for the same local file (the same episode on two trackers, a single episode and a pack). |
| Primary | The `full` proposal of a group that `accept-group` accepts. Elected automatically or chosen by the operator. |
| Conflict (`conflict`) | Proposal that lists several possible torrents for the same file. It cannot be accepted: a torrent is chosen. |
| Link directory | Folder from `linkDirs` where Link rebuilds each torrent. |
| Torrent root | Folder from `dedupe.torrentRoots` that deduplication scans. |
| Link | Hardlink, symlink or reflink created by the tool and recorded in the database. |
| Stage lock | Single database lock: only one stage, `dedupe` action or `repair` action runs at a time. |
| Job | Long action started by the web API; one at a time, 50 kept in memory. |
| Force | Explicit start of a `partial_annex` left paused. |
| cross-seed | External daemon that looks for other trackers for a torrent; Necromanciarr reads it (`config.js`) and notifies it, or replaces it (`native` mode). |

## Conventions

- A proposal, group or job identifier is a strictly positive integer.
- `config.json` keys are in `camelCase`, the JSON fields of CLI and API outputs in `snake_case`.
- `--json` outputs and the API contain no secret: tokens, API keys, passwords, the key of qui's `/proxy/` path and rc credentials are masked with `***` or `[redacted]`.
- Texts meant for the operator (error messages, refusal reasons, `doctor` texts) follow the `language` setting; codes, JSON field names, commands and options stay in English, and a few texts are not translated yet ([04-cli-and-web.md](04-cli-and-web.md)). The pages of this reference exist in French ([../00-index.md](../00-index.md)) and in English.
- Examples use fictitious values: `/home/u/…`, `/data/…`, `example.com`, `alice`.
- A configuration duration is a Go duration (`10m`, `90s`, `1h`).

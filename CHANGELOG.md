# Changelog

Format inspiré de [Keep a Changelog](https://keepachangelog.com/fr/1.1.0/).

## [Unreleased]

### Added
- First release: finds on a library provider the torrents whose files the library already holds (through Radarr and Sonarr), proves every file by the piece SHA-1, rebuilds the torrent with links in a separate link folder and seeds it in qBittorrent; nothing happens without an accepted proposal and the library is never written.
- Proposals for full torrents, partial annexes, partial packs, conflicts and native cross-seed, with groups and a chosen primary; Force for a paused partial annex.
- Torrent-side deduplication (`dedupe plan/apply`) and broken-link repair (`repair scan/apply`), guarded by a client recheck.
- Cross-seed integration: sandboxed import of its `config.js`, `auto/delegate/native/off` modes, webhook notification.
- rclone hash providers (`fsProviders`) with a per-mount capability cache.
- CLI with `--json`, web interface and API (bearer token, jobs), `doctor` diagnostics, Docker image (amd64, arm64, riscv64).

### Security
- Force copies each linked media sharing a missing piece out of the library before starting, or refuses.
- Rechecks resume only torrents that were running; an incomplete torrent added by Seed must show paused.
- Text output strips terminal control characters; `config.js` cannot read this tool's variables; response bodies are capped; redirects are refused.

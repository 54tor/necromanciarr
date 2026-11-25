# Docker and volumes

## The image

Built by `make docker-build` (Node stage for the interface, static Go stage, final stage `debian:trixie-slim` without a `RUN` instruction). `make docker-buildx` produces an OCI archive for `linux/amd64`, `linux/arm64` and `linux/riscv64` without pushing or emulating; `make cross-build` produces the binaries for these three Linux targets and for `windows/amd64`, `windows/arm64`.

| Image setting | Value |
|---|---|
| User | `1000:1000`, non-root |
| Entry point / command | `necromanciarr` / `serve`: any argument replaces `serve` |
| Variables | `CONFIG_DIR=/data`, `NECROMANCIARR_LISTEN=0.0.0.0:8787` |
| Port | `EXPOSE 8787` |
| Working directory and volume | `/data`, owned by `1000:1000` in the image |
| Health probe | `necromanciarr healthcheck` every 30 s, timeout 5 s, start period 20 s, 3 retries |
| Contents | base `debian:trixie-slim` (dated tag), the binary and the TLS certificates; time zones embedded in the binary |

The image listens on all interfaces, so **`NECROMANCIARR_AUTH_TOKEN` is required for `serve`** (the default command): without it the container exits with code 1 and `listen "0.0.0.0:8787" is not loopback: set NECROMANCIARR_AUTH_TOKEN (at least 32 characters, generate one with necromanciarr api-key)`. Generate a token: `docker run --rm necromanciarr:dev api-key`.

## Volumes

| Mount | Mode | Role |
|---|---|---|
| `/data` | read-write, named volume | `config.json`, `.env`, `necromanciarr.db` (relative `dbPath`, resolved from `CONFIG_DIR`) |
| library **and** link directories | read-write, **a single mount** | media files and the `linkDirs` / `dedupe.torrentRoots` folders |
| cross-seed folder | **read-only**, whole folder | `config.js` |

### A single mount for the library and the links

A hardlink or reflink between two distinct mounts fails with `EXDEV`, even when they point to the same host disk: the kernel compares mounts, not disks. Mount the common parent once and place the roots and link directories under it:

```
/srv/storage            (hôte)  ->  /media  (conteneur, un seul montage)
  library/movies                      libraryRoots[0]  = /media/library/movies
  library/tv                          libraryRoots[1]  = /media/library/tv
  links                               linkDirs[0]      = /media/links
```

`doctor` (`link-feasibility`) fails a hardlink or reflink between different volumes and suggests "one bind mount covering both". The mount is writable because the links are created there; that no library file is written comes down to the code's invariants ([03-seed-safety.md](03-seed-safety.md)), not to the mount. A link directory cannot be inside a library root: place it next to it, under the same parent.

The torrent client must read the torrent folder. It sees it under its own path: declare the `pathMappings` rule `{"from": "/media/links", "to": "<chemin vu par le client>"}`. With `linkType: symlink`, the link's target is an absolute path **of this container** (`/media/library/…`): the client must then see the library at the same path, otherwise the link is broken on its side. With a hardlink, the question does not arise.

Remote link folder (`sftp://<provider>/<absolute path>`): the links are created on the remote host, and the torrent client reads that folder either on that host (seedbox) or through a mount of the same storage. The rule translates the remote location to the client's view: `{"from": "sftp://seedbox/srv/links", "to": "/downloads/links"}`. The `from` side is compared as POSIX and case-sensitively, and only covers locations of the same provider; the `to` side stays the path seen by the client, never an `sftp://` location (refused at load time). With no applicable rule, Seed refuses (`client_path_unmapped`) instead of assuming the same path.

### cross-seed's `config.js`

Mount the **folder** that contains `config.js`, read-only, never the file alone: a single-file mount breaks when an editor replaces the file (new inode, the container keeps the old one). Then `CROSS_SEED_CONFIG=/cross-seed/config.js` (or `crossSeedConfig`).

Each `process.env.NAME` that this `config.js` references must also be provided to the Necromanciarr container (environment variable or line of `/data/.env`): the file is evaluated here, with these values, not with the cross-seed container's environment. The `localhost` in `config.js` designates the cross-seed container: set `crossSeed` (`http://cross-seed:2468?apikey=${CROSS_SEED_API_KEY}`) to reach the daemon.

### `/data`

A volume name rather than a host folder: a new named volume takes on the image's `1000:1000` owner. A host folder must belong to the container's user. To put `config.json` and `.env` in it:

```
docker run --rm --user 1000:1000 -v necromanciarr-data:/data -v "$PWD":/src:ro \
  --entrypoint cp necromanciarr:dev /src/config.json /src/.env /data/
```

### Permissions

The container runs as `1000:1000`. For a library owned by another account, override `user: "<uid>:<gid>"` with the one that can read the library, write in the link directories and in `/data`. The torrent client must read the source files through the links: the library's permissions apply to it too (a hardlink keeps the file's owner and permissions). Torrent folders are created as `0755`.

## Example

```yaml
services:
  necromanciarr:
    image: necromanciarr:dev
    user: "1000:1000"
    restart: unless-stopped
    ports:
      - "127.0.0.1:8787:8787"
    environment:
      NECROMANCIARR_AUTH_TOKEN: ${NECROMANCIARR_AUTH_TOKEN:?générer avec necromanciarr api-key}
      CROSS_SEED_CONFIG: /cross-seed/config.js
      # process.env.* référencés par le config.js de cross-seed
      RADARR_API_KEY: ${RADARR_API_KEY}
      SONARR_API_KEY: ${SONARR_API_KEY}
      QBIT_PASSWORD: ${QBIT_PASSWORD}
    volumes:
      - necromanciarr-data:/data
      - /srv/storage:/media
      - /srv/appdata/cross-seed:/cross-seed:ro
    cpus: 2
    mem_limit: 2g
volumes:
  necromanciarr-data:
```

The matching `/data/config.json`:

```json
{
  "libraryApi": "https://library.example.com",
  "libraryRoots": ["/media/library/movies", "/media/library/tv"],
  "linkDirs": ["/media/links"],
  "pathMappings": [{ "from": "/media/links", "to": "/downloads/links" }],
  "radarr": ["http://radarr:7878?apikey=${RADARR_API_KEY}"],
  "sonarr": ["http://sonarr:8989?apikey=${SONARR_API_KEY}"],
  "torrentClients": ["qbittorrent:http://alice:${QBIT_PASSWORD}@qbittorrent:8080"],
  "crossSeed": "http://cross-seed:2468?apikey=${CROSS_SEED_API_KEY}"
}
```

`LIBRARY_PROVIDER_TOKEN` and `CROSS_SEED_API_KEY` go in `/data/.env`. The port is only published on the host's loopback: remote access goes through a reverse proxy (which keeps the `Bearer` token) or a tunnel.

## Running a command in the container

The entry point is the binary: the argument is the subcommand.

```
docker compose exec necromanciarr necromanciarr verify
docker run --rm --user 1000:1000 -v necromanciarr-data:/data -v /srv/storage:/media \
  necromanciarr:dev doctor --json
```

Only `serve` requires `NECROMANCIARR_AUTH_TOKEN` outside loopback: a one-off command (`config check`, `doctor`, `scan`…) and `healthcheck` run without a token despite the image's `0.0.0.0:8787` listening. A provided token must always be at least 32 characters, whatever the command. Without a token, `doctor` flags the public listening through its `auth` check (`fail`).

A command launched in a second process shares the database and the stage lock with the server: if a stage is already running, it fails with `stage_running` (exit 1) instead of running concurrently. A throwaway `docker run` must mount the same volumes as the service, otherwise it sees an empty database and empty paths.

## rclone mount

A library on an rclone mount is read like a local folder. Notes for the host's mount:

- Mount with `--vfs-links` so that symlinks have a target; without it `doctor` reports "symlinks fail on this mount: restart rclone with --vfs-links". An rclone mount accepts neither hardlinks nor reflinks in practice (`doctor`'s probe decides): the link directories are not on this mount (see [06-limits-and-troubleshooting.md](06-limits-and-troubleshooting.md)).
- If rclone is restarted on the host, the container only sees the new mount if the bind mount propagates mounts (`bind.propagation: rslave` in Compose); otherwise restart the container.
- Start the process that carries the mount with `--rc --rc-addr :5572 --rc-user <utilisateur> --rc-pass <motdepasse>` and declare `fsProviders[].rc` towards `rclone:5572` on a private network: without rc, no fingerprint is available and deduplication rereads the files.
- rclone's VFS cache can make a modification date look like one returned by the storage: `doctor` writes it as advice.

## SFTP provider

An `fsProviders` entry of type `sftp` ([02-configuration.md](02-configuration.md)) needs no mount for the remote storage: only its trust and key files enter the container.

- Mount `known_hosts` and the private key read-only, for example `- /home/u/necromanciarr/ssh:/data/ssh:ro`, and declare `knownHosts: "/data/ssh/known_hosts"`, `identityFile: "/data/ssh/id_ed25519"` (paths as seen by the container). The key must be readable by user `1000:1000` and stay at `0600`.
- The key passphrase or the password go through the `.env` in `/data` (`SFTP_KEY_PASSPHRASE=…`, referenced as `"${SFTP_KEY_PASSPHRASE}"`), never in clear text in `config.json`.
- The host's SSH agent (`SSH_AUTH_SOCK`) is not forwarded to the container: `agent: true` requires mounting the socket and setting `SSH_AUTH_SOCK` yourself, which the image does not do. Prefer `identityFile`.
- Recommended: an SFTP account with no write access to the library (ACL, group, or a read-only mount on the server side), writing only in the link folder, as defence in depth: SFTP has no folder-relative open, so write confinement relies on path checks ("Remote writes" in [02-configuration.md](02-configuration.md)). It is not required; a `doctor` check will report it.
- The database (`dbPath`) stays in `/data`, never on the SFTP server.
- Windows SFTP servers: not supported.
- `necromanciarr doctor` checks every provider: `sftp:<name>` (connection, host key, Windows server refused), `sftp-library:<name>` (readable root, media in the first 2000 entries, `library_maybe_writable` warning when modes and owners suggest the account can write, never by a write attempt) and `sftp-exec:<name>` (remote commands), then `link-feasibility` and `path-mappings` for remote link folders ([06-limits-and-troubleshooting.md](06-limits-and-troubleshooting.md)).

### Restricted account and remote commands

`exec` (`auto` by default, `off`, `required`) controls the read-only remote commands (`stat`, `sha1sum`, `md5sum`) the tool sends through `ssh exec` on the same connection; nothing is ever written or linked through exec.

- `auto`: at the first write root of a session (the link folder), the probe checks the login shell and the programs, then compares `stat` with SFTP on its own probe files (size, date within one second, non-zero inode, a hard link seen as one file with at least two links, the folder with another inode) and `sha1sum`, `md5sum` on the byte it has just written. The probe never reads nor touches a library path. When everything agrees: remote file identity (`RemoteStat`) and hashes computed by the host (`RemoteHash`). An identity is trusted only on the device the probe validated (a library mounted on another file system stays without remote inode) and it is read again at each use (one exec session per file, never cached). Otherwise (refusal, missing program, non-GNU coreutils, inconsistent answer): SFTP only and a single `remote_exec_unavailable` warning per volume and per session.
- `off`: no command, no warning. `required`: a failed probe refuses the volume's write roots (`remote_exec_required`); Link keeps the proposal for the next run, dedupe makes it not applicable.
- What is lost without exec: no remote inode (repair `inode` unavailable, a file is never "already linked"), no remote hash (dedupe reads both files in full over SFTP, at the account's bandwidth, `warning.dedupe.full_remote_read`).
- With exec: the hash is computed on the host (the server reads each file once, under `nice` and `ionice` when present; `remote_hash_bytes` announces the volume). A remote inode is only compared with another inode of the same volume, never with a local file or another provider; it never proves a link (`identity_unproven`, no `skip_checking`) and a repair found by remote inode is proposed, never applied automatically.
- A command that fails after a successful probe (vanished file, timeout) gives `remote_exec_failed` for that item, read again over SFTP; dedupe `verify: auto` then moves to a full read, never to samples.
- Recommended account: read-only on the library, writing only in the link folder, and a dedicated key bound by a forced guard command in `authorized_keys`: [remote-command-guard.sh](../remote-command-guard.sh) (installation at the top of the script, checked by `shellcheck`). It lets through only the SFTP server and the exact lines of the table, which `necromanciarr volumes commands <nom>` also prints.

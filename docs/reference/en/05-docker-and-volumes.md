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

Remote link folder (`sftp://<provider>/<absolute path>`, with the filesystem providers to come): the links are created on the remote host, and the torrent client reads that folder either on that host (seedbox) or through a mount of the same storage. The rule translates the remote location to the client's view: `{"from": "sftp://seedbox/srv/links", "to": "/downloads/links"}`. The `from` side is compared as POSIX and case-sensitively, and only covers locations of the same provider; the `to` side stays the path seen by the client, never an `sftp://` location (refused at load time). With no applicable rule, Seed refuses (`client_path_unmapped`) instead of assuming the same path.

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

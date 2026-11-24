# Interface web : étape indépendante de l'architecture cible (le JS construit est identique partout),
# donc exécutée sur la machine de build. Tag Node exact, vérifié avec docker manifest inspect.
# --ignore-scripts : aucune dépendance n'a besoin de script d'installation.
FROM --platform=$BUILDPLATFORM node:26.13.1-trixie-slim AS ui
WORKDIR /src/web
COPY web/package.json web/package-lock.json ./
RUN npm ci --ignore-scripts --no-audit --no-fund
COPY web ./
# vite.config.ts écrit dans ../internal/web/ui/dist, soit /src/internal/web/ui/dist.
RUN npm run build

# Compilation croisée native : l'étape Go tourne sur la machine de build, jamais sous émulation.
# Tag Go aligné sur la version de go.mod.
FROM --platform=$BUILDPLATFORM golang:1.27.4-trixie AS builder
ARG TARGETOS
ARG TARGETARCH
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# Remplace le marqueur de remplacement par l'interface construite.
COPY --from=ui /src/internal/web/ui/dist ./internal/web/ui/dist
# Binaire statique et reproductible ; /data est préparé ici car l'étape finale n'a aucun RUN.
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
RUN GOOS=$TARGETOS GOARCH=$TARGETARCH go build -o /out/necromanciarr ./cmd/necromanciarr \
    && mkdir /data

# Image d'exécution minimale, sans instruction RUN (aucun QEMU/binfmt requis pour les autres architectures).
# Les fuseaux horaires sont embarqués dans le binaire (time/tzdata) ; les certificats viennent de l'étape Go.
# Tag daté épinglé : pour rafraîchir, choisir un tag trixie-AAAAMMJJ-slim récent sur Docker Hub,
# vérifier qu'il existe (docker manifest inspect debian:<tag>) puis le remplacer ici.
FROM debian:trixie-20261027-slim
COPY --from=builder /etc/ssl/certs/ca-certificates.crt /etc/ssl/certs/ca-certificates.crt
COPY --from=builder /out/necromanciarr /usr/local/bin/necromanciarr
# La base SQLite par défaut vit dans /data (clé dbPath).
COPY --from=builder --chown=1000:1000 /data /data
# UID/GID 1000 par défaut, surchargeables par user: au lancement pour coller aux droits de la bibliothèque.
USER 1000:1000
# config.json, .env et api-key sont lus et écrits dans le volume.
ENV CONFIG_DIR=/data
WORKDIR /data
VOLUME /data
ENTRYPOINT ["/usr/local/bin/necromanciarr"]
CMD ["serve"]

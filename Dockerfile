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
# Attribution MIT/BSD/MPL exigée par les dépendances liées dans le binaire et le bundle web.
COPY LICENSE THIRD_PARTY_NOTICES.md /usr/share/doc/necromanciarr/
# La base SQLite par défaut vit dans /data (clé dbPath).
COPY --from=builder --chown=1000:1000 /data /data
# UID/GID 1000 par défaut, surchargeables par user: au lancement pour coller aux droits de la bibliothèque.
USER 1000:1000
# config.json et .env sont lus dans le volume. L'écoute sur toutes les interfaces rend l'interface joignable depuis
# l'hôte ; la configuration refuse alors de démarrer sans NECROMANCIARR_AUTH_TOKEN (32 caractères au moins).
ENV CONFIG_DIR=/data NECROMANCIARR_LISTEN=0.0.0.0:8787
EXPOSE 8787
WORKDIR /data
VOLUME /data
# Sonde de vivacité : la sous-commande interroge /healthz sur la boucle locale (forme exec, ni curl ni wget dans l'image).
HEALTHCHECK --interval=30s --timeout=5s --start-period=20s --retries=3 CMD ["necromanciarr", "healthcheck"]
# Sans argument, l'image sert l'interface web ; toute autre sous-commande remplace CMD.
ENTRYPOINT ["/usr/local/bin/necromanciarr"]
CMD ["serve"]

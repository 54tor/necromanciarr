# Tag Go aligné sur la version de go.mod.
FROM golang:1.27.4-trixie AS builder
WORKDIR /src
COPY go.mod go.sum ./
RUN go mod download
COPY cmd ./cmd
COPY internal ./internal
# Binaire statique et reproductible.
ENV CGO_ENABLED=0 GOFLAGS=-trimpath
RUN go build -o /out/necromanciarr ./cmd/necromanciarr

# Image d'exécution minimale ; la base SQLite par défaut vit dans /data (clé db_path).
FROM debian:trixie-slim
RUN apt-get update \
    && apt-get install --no-install-recommends -y ca-certificates tzdata \
    && rm -rf /var/lib/apt/lists/* \
    && mkdir /data \
    && chown 1000:1000 /data
COPY --from=builder /out/necromanciarr /usr/local/bin/necromanciarr
# UID/GID 1000 par défaut, surchargeables par user: au lancement pour coller aux droits de la bibliothèque.
USER 1000:1000
WORKDIR /data
VOLUME /data
ENTRYPOINT ["/usr/local/bin/necromanciarr"]
CMD ["serve"]

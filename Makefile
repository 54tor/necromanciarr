# Plafond de parallélisme (CPU) pour tous les outils lourds ; 75 % de la machine au plus.
QUALITY_PARALLELISM ?= 4
IMAGE ?= necromanciarr:dev
# nice/ionice : ne pas écraser les autres sessions du poste.
GO_RUN = GOMAXPROCS=$(QUALITY_PARALLELISM) nice -n 10 ionice -c3 go
# Dépôt monté en lecture seule ; ressources du conteneur bornées.
DOCKER_TOOL = nice -n 10 ionice -c3 docker run --rm --cpus 2 --memory 2g --user $$(id -u):$$(id -g) -v "$(CURDIR)":/src:ro -w /src

# Tags épinglés sans digest : le tag exact suffit à la reproductibilité de ces outils.
HADOLINT_IMAGE = hadolint/hadolint:v2.14.0
SHELLCHECK_IMAGE = koalaman/shellcheck:v0.11.0
SHFMT_IMAGE = mvdan/shfmt:v3.12.0
GITLEAKS_IMAGE = zricethezav/gitleaks:v8.30.0
ACTIONLINT_IMAGE = rhysd/actionlint:1.7.8

# Tous les scripts shell suivis, hooks compris.
SHELL_SCRIPTS = $(shell git ls-files '*.sh' '.githooks/*')

# Commandes surchargeables (make fmt GOFUMPT_CMD=false) pour prouver que l'échec d'un outil fait échouer la cible.
GOFUMPT_CMD ?= $(GO_RUN) tool gofumpt
GOIMPORTS_CMD ?= $(GO_RUN) tool goimports
DEADCODE_CMD ?= $(GO_RUN) tool deadcode
PACKAGE_LIST_CMD ?= $(GO_RUN) list ./...
QUALITY_TARGETS = fmt imports vet staticcheck lint test vuln sec mod deadcode shell hadolint gitleaks actionlint

.PHONY: build quality hooks docker-build docker-lint site $(QUALITY_TARGETS)

build:
	CGO_ENABLED=0 $(GO_RUN) build -p $(QUALITY_PARALLELISM) ./cmd/necromanciarr

# Lance toute la pile sans s'arrêter au premier échec, puis liste les cibles en échec.
quality:
	@failed=""; \
	for target in $(QUALITY_TARGETS); do \
		echo "== $$target"; \
		$(MAKE) -s --no-print-directory $$target || failed="$$failed $$target"; \
	done; \
	if [ -n "$$failed" ]; then echo "quality FAILED:$$failed"; exit 1; fi; \
	echo "quality OK"

fmt:
	@out=$$($(GOFUMPT_CMD) -l .) || exit 1; test -z "$$out" || { echo "$$out"; exit 1; }

imports:
	@out=$$($(GOIMPORTS_CMD) -l .) || exit 1; test -z "$$out" || { echo "$$out"; exit 1; }

vet:
	$(GO_RUN) vet -p $(QUALITY_PARALLELISM) ./...

staticcheck:
	$(GO_RUN) tool staticcheck ./...

lint:
	$(GO_RUN) tool golangci-lint run --concurrency $(QUALITY_PARALLELISM)

# Un paquet à la fois : -race multiplie la RAM et le CPU.
test:
	@packages=$$($(PACKAGE_LIST_CMD)) || exit 1; \
	test -n "$$packages" || { echo "aucun paquet à tester"; exit 1; }; \
	status=0; \
	for package in $$packages; do \
		$(GO_RUN) test -race -cover -p $(QUALITY_PARALLELISM) $$package || status=1; \
	done; \
	exit $$status

vuln:
	$(GO_RUN) tool govulncheck ./...

# G101 : faux positifs sur les noms de variables d'environnement (exclusion globale).
# G304 et SHA-1 (G401/G505) sont annotés au cas par cas dans le code.
sec:
	$(GO_RUN) tool gosec -quiet -exclude=G101 ./...

mod:
	$(GO_RUN) mod tidy -diff
	$(GO_RUN) mod verify

# -test : le binaire ne câble pas encore les paquets ; les tests servent de racines.
deadcode:
	@out=$$($(DEADCODE_CMD) -test ./...) || exit 1; test -z "$$out" || { echo "$$out"; exit 1; }

shell:
	$(DOCKER_TOOL) $(SHELLCHECK_IMAGE) $(SHELL_SCRIPTS)
	$(DOCKER_TOOL) $(SHFMT_IMAGE) -d $(SHELL_SCRIPTS)

hadolint:
	$(DOCKER_TOOL) $(HADOLINT_IMAGE) hadolint Dockerfile

gitleaks:
	$(DOCKER_TOOL) $(GITLEAKS_IMAGE) git --no-banner --redact /src

actionlint:
	$(DOCKER_TOOL) $(ACTIONLINT_IMAGE) -color

# Site de documentation (VitePress) : Node reste confiné à docs/site.
site:
	cd docs/site && nice -n 10 ionice -c3 npm ci && nice -n 10 ionice -c3 npm run build
	cd docs/site && npm audit --audit-level=high

docker-build:
	nice -n 10 ionice -c3 docker build -t $(IMAGE) .

docker-lint: hadolint

# Active les hooks versionnés du dépôt (pre-push lance make quality).
hooks:
	git config core.hooksPath .githooks

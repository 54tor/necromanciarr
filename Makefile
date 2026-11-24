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
QUALITY_TARGETS = fmt imports vet staticcheck lint test vuln sec mod deadcode shell hadolint gitleaks actionlint docs-check ui-lint ui-test ui-audit ui-csp

# Chaîne front (web/) : tout est no-op tant que web/package.json n'existe pas.
# Quand il existe, l'absence de Node/npm fait échouer la cible avec un message explicite.
# npm_config_maxsockets borne les téléchargements parallèles de npm.
WEB_DIR = web
NPM = nice -n 10 ionice -c3 env npm_config_maxsockets=$(QUALITY_PARALLELISM) npm --prefix $(WEB_DIR)
# Garde : sort 0 sans rien faire si web/ est absent ; échoue si npm manque ou si Node est trop ancien.
# Minimum aligné sur « engines » de web/package.json (>= 22.12).
UI_GUARD = @test -f $(WEB_DIR)/package.json || { echo "web/package.json absent : cible front ignorée"; exit 0; }; \
	command -v npm >/dev/null && command -v node >/dev/null || { echo "Node/npm requis (web/package.json existe) : installer Node >= 22.12"; exit 1; }; \
	node -e 'const [major, minor] = process.versions.node.split(".").map(Number); process.exit(major > 22 || (major === 22 && minor >= 12) ? 0 : 1)' \
		|| { echo "Node trop ancien : version >= 22.12 requise"; exit 1; }

.PHONY: ui ui-deps ui-lint ui-test ui-audit ui-csp build cross-build docker-buildx quality hooks docker-build docker-lint site docs-check $(QUALITY_TARGETS)

build: ui
	CGO_ENABLED=0 $(GO_RUN) build -p $(QUALITY_PARALLELISM) ./cmd/necromanciarr

# Plateformes livrées ; un seul processus à la fois, go vet par plateforme.
CROSS_PLATFORMS = linux/amd64 linux/arm64 linux/riscv64 windows/amd64 windows/arm64
BUILDX_PLATFORMS = linux/amd64,linux/arm64,linux/riscv64

cross-build:
	@mkdir -p dist; \
	for platform in $(CROSS_PLATFORMS); do \
		goos=$${platform%/*}; goarch=$${platform#*/}; \
		extension=""; test "$$goos" = windows && extension=".exe"; \
		output=dist/necromanciarr-$$goos-$$goarch$$extension; \
		echo "== $$platform"; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch $(GO_RUN) vet -p $(QUALITY_PARALLELISM) ./... || exit 1; \
		CGO_ENABLED=0 GOOS=$$goos GOARCH=$$goarch $(GO_RUN) build -trimpath -p $(QUALITY_PARALLELISM) -o $$output ./cmd/necromanciarr || exit 1; \
		file $$output; \
		go version -m $$output | head -1; \
	done

# Sortie OCI locale, sans push : le builder docker par défaut ne sait pas charger du multi-plateforme
# dans le moteur, d'où l'archive.
docker-buildx:
	@mkdir -p dist
	nice -n 10 ionice -c3 docker buildx build --platform $(BUILDX_PLATFORMS) --output type=oci,dest=dist/necromanciarr-oci.tar .

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

# Paquets du module listés explicitement : ./... descendrait dans docs/site/node_modules.
# -test : les tests servent aussi de racines ; quelques options et enveloppes ne sont appelées que par les tests de
# fonctions gardées (options de délai de qbit, Coverage, DescribeMount...), sans être du code mort.
deadcode:
	@out=$$($(DEADCODE_CMD) -test ./cmd/... ./internal/...) || exit 1; test -z "$$out" || { echo "$$out"; exit 1; }

shell:
	$(DOCKER_TOOL) $(SHELLCHECK_IMAGE) $(SHELL_SCRIPTS)
	$(DOCKER_TOOL) $(SHFMT_IMAGE) -d $(SHELL_SCRIPTS)

hadolint:
	$(DOCKER_TOOL) $(HADOLINT_IMAGE) hadolint Dockerfile

gitleaks:
	$(DOCKER_TOOL) $(GITLEAKS_IMAGE) git --no-banner --redact /src

actionlint:
	$(DOCKER_TOOL) $(ACTIONLINT_IMAGE) -color

# Installation reproductible depuis le lockfile, sans scripts d'installation (aucune dépendance n'en a besoin).
ui-deps:
	$(UI_GUARD); \
	if [ ! -f $(WEB_DIR)/node_modules/.package-lock.json ] || [ $(WEB_DIR)/package-lock.json -nt $(WEB_DIR)/node_modules/.package-lock.json ]; then \
		$(NPM) ci --ignore-scripts --no-audit --no-fund; \
	fi

# Construit l'interface vers internal/web/ui/dist (embarqué dans le binaire).
# Le marqueur .placeholder suivi par git est recopié par Vite : le dépôt reste propre après la construction.
ui: ui-deps
	$(UI_GUARD); \
	$(NPM) run build

ui-lint: ui-deps
	$(UI_GUARD); \
	$(NPM) run typecheck && $(NPM) run lint && $(NPM) run format:check && $(NPM) run knip

ui-test: ui-deps
	$(UI_GUARD); \
	$(NPM) test -- --maxWorkers=$(QUALITY_PARALLELISM)

ui-audit: ui-deps
	$(UI_GUARD); \
	$(NPM) run audit

# La CSP du serveur interdit unsafe-eval : le bundle construit ne doit ni évaluer ni compiler de texte, et
# index.html ne doit porter aucun script en ligne (le test Go de internal/web ne couvre que les builds présents).
UI_DIST = internal/web/ui/dist
ui-csp: ui
	$(UI_GUARD); \
	if grep -nE '\beval\b|\bFunction\(|new Function' $(UI_DIST)/assets/*.js; then \
		echo "ui-csp : eval ou Function() dans le bundle, interdit par la CSP du serveur"; exit 1; \
	fi; \
	if grep -l '__msg__' $(UI_DIST)/assets/*.js; then \
		echo "ui-csp : compilateur de messages vue-i18n embarqué (dropMessageCompiler perdu)"; exit 1; \
	fi; \
	if grep -E '<script' $(UI_DIST)/index.html | grep -vq 'src='; then \
		echo "ui-csp : script en ligne dans index.html, interdit par la CSP du serveur"; exit 1; \
	fi

# Référence française : aucune identité ni chemin local, aucun lien relatif cassé (script sans dépendance).
docs-check:
	docs/reference/verifier-docs.test.sh docs/reference

# Site de documentation (VitePress) : Node reste confiné à docs/site.
site:
	docs/site/garde-identite.test.sh docs/site
	cd docs/site && nice -n 10 ionice -c3 npm ci && nice -n 10 ionice -c3 npm run build
	cd docs/site && npm audit --audit-level=high

docker-build: ui
	nice -n 10 ionice -c3 docker build -t $(IMAGE) .

docker-lint: hadolint

# Active les hooks versionnés du dépôt (pre-push lance make quality).
hooks:
	git config core.hooksPath .githooks

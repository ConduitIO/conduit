# Version will extract the current version of Conduit based on
# the latest git tag and commit. If the repository contains any
# changes the version will have the suffix "-dirty", it will
# ignore any untracked files though to ensure Docker builds have
# the correct version.
VERSION=`git describe --tags --dirty`
GO_VERSION_CHECK=`./scripts/check-go-version.sh`
# GOPATH_BIN is where `make install-tools` installs the tools/go.mod-pinned
# binaries. `make verify` invokes them by explicit path so a differently-versioned
# tool on PATH cannot shadow the one CI uses.
GOPATH_BIN=$(shell go env GOPATH)/bin
# MARKDOWNLINT_VERSION must match the markdownlint-cli2 version the CI action
# resolves to (.github/workflows/markdown-lint.yml). Keep the two in sync — this
# is the single knob that stops local/CI markdownlint drift.
MARKDOWNLINT_VERSION=0.18.1
# The \# escapes are required: an unescaped # starts a Make comment. markdownlint-cli2
# reads a leading # in a glob as "exclude this path".
MARKDOWNLINT_GLOBS="**/*.md" "\#LICENSE.md" "\#pkg/http/openapi/**" "\#.github/*.md" "\#pkg/scaffold/template/**"

# The build target should stay at the top since we want it to be the default target.
.PHONY: build
build: check-go-version
	go build -ldflags "-X 'github.com/conduitio/conduit/pkg/conduit.version=${VERSION}'" -o conduit ./cmd/conduit/main.go
	@echo "\nBuild complete. Enjoy using Conduit!"
	@echo "Get started by running:"
	@echo " ./conduit run"

.PHONY: test
test:
	go test $(GOTEST_FLAGS) -race ./...

.PHONY: escape-analysis
escape-analysis:
	go test -gcflags "-m -m"  $(GOTEST_FLAGS) ./... 2> escape_analysis_full.txt
	grep -vwE "(.*_test\.go|.*\/mock/.*\.go)" escape_analysis_full.txt > escape_analysis.txt
	rm escape_analysis_full.txt

.PHONY: test-integration
test-integration:
	# run required docker containers, execute integration tests, stop containers after tests
	docker compose -f test/compose-postgres.yaml -f test/compose-schemaregistry.yaml up --quiet-pull -d --wait
	go test $(GOTEST_FLAGS) -race --tags=integration ./...; ret=$$?; \
		docker compose -f test/compose-postgres.yaml -f test/compose-schemaregistry.yaml down; \
		exit $$ret

.PHONY: fmt
fmt:
	gofumpt -l -w .

.PHONY: lint
lint:
	golangci-lint run

.PHONY: run
run:
	go run ./cmd/conduit/main.go

# ui reproduces pkg/web/ui/dist/ from a checkout of the separate conduit-ui
# repo (https://github.com/ConduitIO/conduit-ui), so it is a deliberate,
# reviewed re-embed step, not part of the default build. Usage:
#   make ui UI_REPO=/path/to/conduit-ui
# or, to clone a specific ref fresh:
#   make ui UI_REF=<commit-or-tag>
# UI_REF/UI_REPO default to a fresh clone of main — always pass UI_REF (or a
# checked-out UI_REPO) when re-embedding for a release, so the pin is
# intentional and recorded in the commit message, not "whatever main was."
UI_REPO ?=
UI_REF ?= main
.PHONY: ui
ui:
	@set -e; \
	if [ -n "$(UI_REPO)" ]; then \
		src="$(UI_REPO)"; \
		echo "==> using existing checkout at $$src"; \
	else \
		src=$$(mktemp -d); \
		echo "==> cloning conduit-ui@$(UI_REF) into $$src"; \
		git clone --quiet --depth 1 --branch $(UI_REF) https://github.com/ConduitIO/conduit-ui "$$src"; \
	fi; \
	( cd "$$src" && npm ci && npm run build ); \
	rm -rf pkg/web/ui/dist; \
	mkdir -p pkg/web/ui/dist; \
	cp -R "$$src"/dist/. pkg/web/ui/dist/; \
	find pkg/web/ui/dist -name '*.map' -delete; \
	commit=$$(cd "$$src" && git rev-parse HEAD); \
	echo "==> embedded conduit-ui@$$commit into pkg/web/ui/dist (source maps stripped)"; \
	echo "    record this commit in the PR/commit message that updates dist/"

.PHONY: proto-generate
proto-generate:
	cd proto && buf generate

.PHONY: proto-update
proto-update:
	cd proto && buf dep update

.PHONY: proto-lint
proto-lint:
	cd proto && buf lint

.PHONY: clean
clean:
	@rm -f conduit

.PHONY: download
download:
	@echo Download go.mod dependencies
	@go mod download

.PHONY: install-tools
install-tools: download
	@echo Installing tools from tools/go.mod
	@go list -modfile=tools/go.mod tool | xargs -I % go list -modfile=tools/go.mod -f "%@{{.Module.Version}}" % | xargs -tI % go install %

.PHONY: generate
generate:
	go generate -x ./...

# generate-llms is a convenience alias for local iteration on
# cmd/conduit/internal/llmsgen; `make generate`'s go:generate directive
# already produces llms.txt/llms-full.txt, and CI's validate-generated-files
# job calls `make generate`, not this target.
.PHONY: generate-llms
generate-llms:
	go run ./cmd/conduit/internal/llmsgen

.PHONY: check-go-version
check-go-version:
	@if [ "${GO_VERSION_CHECK}" != "" ]; then\
		echo "${GO_VERSION_CHECK}";\
		exit 1;\
	fi

.PHONY: markdown-lint
markdown-lint:
	npx --yes markdownlint-cli2@$(MARKDOWNLINT_VERSION) $(MARKDOWNLINT_GLOBS)

# verify runs the CI-equivalent checks locally, using the same tool-resolution
# CI uses (tools/go.mod for Go tools, a pinned npx for markdownlint) rather than
# trusting ambient PATH, so a green `make verify` predicts a green CI. It is a
# fast pre-push gate, not a full CI replacement: it runs the unit subset
# (-short, no docker), leaving the integration suite and flake-hunt to CI.
.PHONY: verify
verify: install-tools
	@echo "==> lint"
	@$(GOPATH_BIN)/golangci-lint run
	@echo "==> markdown-lint"
	@npx --yes markdownlint-cli2@$(MARKDOWNLINT_VERSION) $(MARKDOWNLINT_GLOBS)
	@echo "==> validate-generated-files"
	@$(MAKE) proto-generate generate
	@git diff --exit-code --numstat
	@echo "==> test (unit subset, -race -short, no docker)"
	@go test -race -short ./...
	@echo "verify: all checks passed"

# setup-hooks opts this clone into the repo's git hooks (a pre-push hook that
# runs `make verify`). Opt-in on purpose: a solo maintainer sometimes wants to
# push a WIP branch without the full gate.
.PHONY: setup-hooks
setup-hooks:
	git config core.hooksPath scripts/hooks
	@echo "git hooks enabled (core.hooksPath=scripts/hooks); pre-push now runs 'make verify'"

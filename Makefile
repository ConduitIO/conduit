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

# coverage writes a Go cover profile for the unit tests under pkg/ (no Docker, no
# integration tag). CI feeds it to .github/scripts/coverage_ratchet.py against
# .github/coverage-baseline.txt; run that script locally to see the gate's verdict.
COVER_OUT ?= cover.out

.PHONY: coverage
coverage:
	go test $(GOTEST_FLAGS) -count=1 -coverprofile=$(COVER_OUT) ./pkg/...
	@echo "Check against the baseline: python3 .github/scripts/coverage_ratchet.py $(COVER_OUT) .github/coverage-baseline.txt"

# bench-engine runs the in-process engine benchmarks (fake connectors, no Docker)
# on both pipeline engines. Output is plain `go test -bench` text, which is what
# benchstat reads: save one run per revision and compare with bench-engine-compare.
# It measures engine overhead, not connector throughput; throughput claims need
# benchi (see docs/architecture/guarantees.md and CONTRIBUTING.md). One run is a
# snapshot; to compare revisions use bench-engine-ab.
BENCH_ENGINE_COUNT ?= 6
BENCH_ENGINE_TIME ?= 3x
BENCH_ENGINE_OUT ?= bench-engine.txt
# benchstat is not a repo dependency: it is fetched on demand with `go run`. The
# pin is a pseudo-version (benchstat has no tagged releases), and its go.mod
# requires Go 1.26 or newer, so an older toolchain will fail to build it.
BENCHSTAT_VERSION ?= v0.0.0-20261009192801-be2c69fb417e

.PHONY: bench-engine
bench-engine:
	go test -run '^$$' -bench '^BenchmarkEngine$$' -benchtime=$(BENCH_ENGINE_TIME) -count=$(BENCH_ENGINE_COUNT) ./pkg/internal/enginetest/engines | tee $(BENCH_ENGINE_OUT)

# bench-engine-ab compares the working tree against BASE (default origin/main)
# with interleaved runs and an A/A control: each round runs three binaries (the
# base, the working tree, and a second copy of the base as the control) in an
# order that rotates every round, so ROUNDS should be a multiple of 3 (default 6)
# for every binary to run in every position equally often. Use it rather than two sequential
# bench-engine runs: drift between invocations shows up as a significant delta on
# identical code. make bench-engine-ab [BASE=origin/main] [ROUNDS=6]
.PHONY: bench-engine-ab
bench-engine-ab:
	./scripts/bench-engine-ab.sh $(or $(BASE),origin/main) $(or $(ROUNDS),6)

# bench-engine-compare prints the deltas and noise between two bench-engine
# outputs: make bench-engine-compare OLD=base.txt NEW=head.txt. Only meaningful
# if the two files came from the same interleaved session (see bench-engine-ab).
.PHONY: bench-engine-compare
bench-engine-compare:
	@test -n "$(OLD)" -a -n "$(NEW)" || { echo "usage: make bench-engine-compare OLD=base.txt NEW=head.txt"; exit 2; }
	go run golang.org/x/perf/cmd/benchstat@$(BENCHSTAT_VERSION) $(OLD) $(NEW)

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

# test-integration-templates runs the vendored pipeline template gallery's
# end-to-end tests for the two infra-dependent templates (postgres-s3,
# postgres-cdc-kafka — see cmd/conduit/root/pipelines/
# template_gallery_e2e_integration_test.go). Deliberately its own target and
# compose file (test/compose-templates.yaml), not folded into
# test-integration: this workstream's Postgres needs logical replication
# enabled (wal_level=logical), which test-integration's shared Postgres
# container must not be changed to support, since other integration tests
# depend on its current config. The two infra-FREE templates
# (generator-log, generator-file) are covered by plain `make test`/
# `make test-integration` already — see template_gallery_e2e_test.go.
.PHONY: test-integration-templates
test-integration-templates:
	docker compose -f test/compose-templates.yaml up --quiet-pull -d --wait
	go test $(GOTEST_FLAGS) -race --tags=templates_e2e ./cmd/conduit/root/pipelines/...; ret=$$?; \
		docker compose -f test/compose-templates.yaml down; \
		exit $$ret

# test-integration-rag-template runs the postgres-pgvector-rag gallery template's
# FULL-ENGINE end-to-end test (cmd/conduit/root/pipelines/
# template_gallery_rag_e2e_integration_test.go, build tag `rag_template_e2e`).
# Its own target and compose file (test/compose-rag-template.yaml), separate from
# test-integration-templates: this template needs a SINGLE pgvector +
# logical-replication Postgres (both CDC source and vector store), which neither
# that target's postgres:17 (no vector extension) nor the rag_e2e pgvector (no
# wal_level=logical) provides.
#
# Requires two sibling checkouts, named via env vars (the two non-postgres
# plugins are not built into conduit and have no install command yet):
#   CONDUIT_PROCESSOR_AI_DIR       -> a ConduitIO/conduit-processor-ai checkout
#   CONDUIT_CONNECTOR_PGVECTOR_DIR -> a ConduitIO/conduit-connector-pgvector checkout
# The test SKIPS cleanly (does not fail) when either is unset — so this target is
# a no-op-by-skip without the checkouts.
.PHONY: test-integration-rag-template
test-integration-rag-template:
	docker compose -f test/compose-rag-template.yaml up --quiet-pull -d --wait
	go test $(GOTEST_FLAGS) -race --tags=rag_template_e2e ./cmd/conduit/root/pipelines/...; ret=$$?; \
		docker compose -f test/compose-rag-template.yaml down; \
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

# check is the fast, Docker-free pre-push hygiene gate. It is the local form of
# CI's `check` job, and it catches, in one run, the four drift classes that
# keep failing merges: (a) stale llms.txt after a connector bump, (b) `go mod
# tidy` wanting changes, (c) gofumpt formatting, (d) markdownlint MD049/MD044.
# It runs, in order: go mod tidy, generate (llms.txt + other go:generate
# output), the CI formatter (gofmt + gofumpt + goimports via `golangci-lint
# fmt` — raw `gofumpt -l .` is NOT used here because it reformats generated
# *.pb.go files that CI skips through .golangci.yml's `generated: lax`), then
# markdownlint, then `git diff --exit-code` so any remaining drift fails the
# run and is shown as a diff. Idempotent: a clean tree stays clean. No Docker,
# no tests.
.PHONY: check
check: install-tools
	@echo "==> go mod tidy"
	@go mod tidy
	@echo "==> generate"
	@PATH="$(GOPATH_BIN):$$PATH" $(MAKE) generate
	@echo "==> format (gofmt + gofumpt + goimports)"
	@$(GOPATH_BIN)/golangci-lint fmt
	@echo "==> markdown-lint"
	@npx --yes markdownlint-cli2@$(MARKDOWNLINT_VERSION) $(MARKDOWNLINT_GLOBS)
	@echo "==> diff"
	@git diff --exit-code --numstat
	@echo "check: all hygiene checks passed"

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

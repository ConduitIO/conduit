# Version will extract the current version of Conduit based on
# the latest git tag and commit. If the repository contains any
# changes the version will have the suffix "-dirty", it will
# ignore any untracked files though to ensure Docker builds have
# the correct version.
VERSION=`git describe --tags --dirty`
GO_VERSION_CHECK=`./scripts/check-go-version.sh`

# The build target should stay at the top since we want it to be the default target.
.PHONY: build
build: check-go-version
	go build -ldflags "-X 'github.com/conduitio/conduit/pkg/conduit.version=${VERSION}'" -o conduit ./cmd/conduit/main.go
	@echo "\nBuild complete. Enjoy using Conduit!"
	@echo "Get started by running:"
	@echo " ./conduit run"

# New targets for built-in connector version management
BUILTIN_VERSION_UPDATER=./bin/update-builtin-version
.PHONY: install-builtin-version-updater
install-builtin-version-updater:
	@go build -o $(BUILTIN_VERSION_UPDATER) ./scripts/update-builtin-version.go

.PHONY: builtin-version
builtin-version: install-builtin-version-updater
	@if [ "$(VERSION_TYPE)" = "release" ]; then \
		echo "Validating BuiltinConnectorsVersion for release tag $(VERSION_TAG)..."; \
		$(BUILTIN_VERSION_UPDATER) release "$(VERSION_TAG)"; \
	elif [ "$(VERSION_TYPE)" = "develop" ]; then \
		echo "Updating BuiltinConnectorsVersion to develop..."; \
		$(BUILTIN_VERSION_UPDATER) develop; \
	else \
		echo "Error: VERSION_TYPE must be 'release' or 'develop'."; \
		exit 1; \
	fi

.PHONY: update-builtin-version-develop
update-builtin-version-develop: install-builtin-version-updater
	@echo "Updating BuiltinConnectorsVersion to develop..."
	$(BUILTIN_VERSION_UPDATER) develop

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

.PHONY: check-go-version
check-go-version:
	@if [ "${GO_VERSION_CHECK}" != "" ]; then\
		echo "${GO_VERSION_CHECK}";\
		exit 1;\
	fi

.PHONY: markdown-lint
markdown-lint:
	markdownlint-cli2 "**/*.md" "#LICENSE.md" "#pkg/web/openapi/**" "#.github/*.md"

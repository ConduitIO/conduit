.PHONY: test test-integration build run proto-update proto-lint clean download install-tools generate check-go-version markdown-lint

# Version will extract the current version of Conduit based on
# the latest git tag and commit. If the repository contains any
# changes the version will have the suffix "-dirty", it will
# ignore any untracked files though to ensure Docker builds have
# the correct version.
VERSION=`git describe --tags --dirty`
GO_VERSION_CHECK=`./scripts/check-go-version.sh`
# Needs to match with what's in .github/workflows/lint.yml
GOLANG_CI_LINT_VER	:= v1.53.3

# The build target should stay at the top since we want it to be the default target.
build: check-go-version pkg/web/ui/dist build-pipeline-check
	go build -ldflags "-X 'github.com/conduitio/conduit/pkg/conduit.version=${VERSION}'" -o conduit -tags ui ./cmd/conduit/main.go
	@echo "\nBuild complete. Enjoy using Conduit!"
	@echo "Get started by running:"
	@echo " ./conduit"

test:
	go test $(GOTEST_FLAGS) -race ./...

test-integration:
	# run required docker containers, execute integration tests, stop containers after tests
	docker compose -f test/docker-compose-postgres.yml -f test/docker-compose-schemaregistry.yml up --quiet-pull -d --wait
	go test $(GOTEST_FLAGS) -race --tags=integration ./...; ret=$$?; \
		docker compose -f test/docker-compose-postgres.yml -f test/docker-compose-schemaregistry.yml down; \
		exit $$ret

.PHONY: golangci-lint-install
golangci-lint-install:
	go install github.com/golangci/golangci-lint/cmd/golangci-lint@$(GOLANG_CI_LINT_VER)

.PHONY: lint
lint: golangci-lint-install
	golangci-lint run -v

build-server: check-go-version
	go build -ldflags "-X 'github.com/conduitio/conduit/pkg/conduit.version=${VERSION}'" -o conduit ./cmd/conduit/main.go
	@echo "build version: ${VERSION}"

build-pipeline-check: check-go-version
	go build -o conduit-pipeline-check ./cmd/conduit-pipeline-check/main.go

run:
	go run ./cmd/conduit/main.go

proto-generate:
	rm -rf proto/gen && cd proto && buf generate

proto-update:
	cd proto && buf mod update

proto-lint:
	cd proto && buf lint

clean:
	@rm -f conduit
	@rm -f conduit-pipeline-check
	@rm -rf pkg/web/ui/dist

download:
	@echo Download go.mod dependencies
	@go mod download

install-tools: download
	@echo Installing tools from tools.go
	@go list -f '{{ join .Imports "\n" }}' tools.go | xargs -tI % go install %
	@go mod tidy

generate:
	go generate ./...

pkg/web/ui/dist:
	make ui-dist

ui-%:
	@cd ui && make $*

check-go-version:
	@if [ "${GO_VERSION_CHECK}" != "" ]; then\
		echo "${GO_VERSION_CHECK}";\
		exit 1;\
	fi

markdown-lint:
	markdownlint-cli2 "**/*.md" "#ui/node_modules" "#LICENSE.md" "#pkg/web/openapi/**" "#.github/*.md"

VERSION=$(shell git describe --tags --dirty --always)

.PHONY: build
build:
	go build -ldflags "-X 'github.com/conduitio/conduit-connector-connectorname.version=${VERSION}'" -o conduit-connector-connectorname cmd/connector/main.go

.PHONY: test
test:
	go test $(GOTEST_FLAGS) -race ./...

.PHONY: test-integration
test-integration:
	# run required docker containers, execute integration tests, stop containers after tests
	docker compose -f test/docker-compose.yml up -d
	go test $(GOTEST_FLAGS) -v -race ./...; ret=$$?; \
		docker compose -f test/docker-compose.yml down; \
		exit $$ret

.PHONY: generate
generate:
	go generate ./...
	conn-sdk-cli readmegen -w

.PHONY: install-tools
install-tools:
	@echo Installing tools from tools/go.mod
	@go list -modfile=tools/go.mod tool | xargs -I % go list -modfile=tools/go.mod -f "%@{{.Module.Version}}" % | xargs -tI % go install %
	@go mod tidy

.PHONY: fmt
fmt:
	gofumpt -l -w .

.PHONY: lint
lint:
	golangci-lint run

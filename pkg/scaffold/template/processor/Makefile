VERSION=$(shell git describe --tags --dirty --always)

.PHONY: build
build:
	GOARCH=wasm GOOS=wasip1 go build -o conduit-processor-processorname.wasm cmd/processor/main.go

.PHONY: test
test:
	go test $(GOTEST_FLAGS) -race ./...

.PHONY: generate
generate:
	go generate ./...

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

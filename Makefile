.PHONY: test test-integration build run proto proto-api proto-plugins proto-lint clean download install-tools generate

VERSION=`./scripts/get-tag.sh`

# The build target should stay at the top since we want it to be the default target.
build: pkg/web/ui/dist
	go build -ldflags "-X 'github.com/conduitio/conduit/pkg/conduit.version=${VERSION}'" -o conduit -tags ui ./cmd/conduit/main.go
	@echo "\nBuild complete. Enjoy using Conduit!"
	@echo "Get started by running:"
	@echo " ./conduit"

test:
	go test $(GOTEST_FLAGS) -race ./...

test-integration:
	# run required docker containers, execute integration tests, stop containers after tests
	docker compose -f test/docker-compose-postgres.yml -f test/docker-compose-kafka.yml up --quiet-pull -d --wait
	go test $(GOTEST_FLAGS) -race --tags=integration ./...; ret=$$?; \
		docker compose -f test/docker-compose-postgres.yml -f test/docker-compose-kafka.yml down; \
		exit $$ret

build-server:
	go build -ldflags "-X 'github.com/conduitio/conduit/pkg/conduit.version=${VERSION}'" -o conduit ./cmd/conduit/main.go
	@echo "build version: ${VERSION}"

run:
	go run ./cmd/conduit/main.go

proto: proto-api proto-plugins

proto-api:
	@echo Generate proto code
	@buf generate

# TODO remove target once plugins are moved to the connector SDK
proto-plugins:
	@echo Generate plugins proto code
	@protoc -I=pkg/plugins/proto \
		--go_out=pkg/plugins/proto      --go_opt=paths=source_relative \
		--go-grpc_out=pkg/plugins/proto --go-grpc_opt=paths=source_relative \
		pkg/plugins/proto/plugins.proto

proto-update:
	@echo Download proto dependencies
	@buf mod update

proto-lint:
	@buf lint

clean:
	@rm -f conduit
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

readme-%:
	go run ./pkg/plugins/template/main.go pkg/plugins/template/readme-template.md $*

.PHONY: build-file-plugin build-pg-plugin build-s3-plugin build-kafka-plugin build-generator-plugin test test-integration build run proto proto-api proto-plugins proto-lint clean download install-tools generate

VERSION=`./scripts/get-tag.sh`

# The build target should stay at the top since we want it to be the default target.
build: ui-dist build-plugins
	go build -ldflags "-X github.com/conduitio/conduit/pkg/conduit.Version=${VERSION}" -o conduit -tags ui ./cmd/conduit/main.go
	@echo "\nBuild complete. Enjoy using Conduit!"
	@echo "Get started by running:"
	@echo " ./conduit"

build-file-plugin:
	go build -o pkg/plugins/file/file pkg/plugins/file/cmd/file/main.go

build-pg-plugin:
	go build -o pkg/plugins/pg/pg pkg/plugins/pg/cmd/pg/main.go

build-s3-plugin:
	go build -o pkg/plugins/s3/s3 pkg/plugins/s3/cmd/s3/main.go

build-kafka-plugin:
	go build -o pkg/plugins/kafka/kafka pkg/plugins/kafka/cmd/kafka/main.go

build-generator-plugin:
	go build -o pkg/plugins/generator/generator pkg/plugins/generator/cmd/generator/main.go

test: build-file-plugin
	go test $(GOTEST_FLAGS) -race ./...

test-integration: build-file-plugin
	# run required docker containers, execute integration tests, stop containers after tests
	docker-compose -f test/docker-compose-postgres.yml -f test/docker-compose-kafka.yml up -d --wait
	go test $(GOTEST_FLAGS) -race --tags=integration ./...; ret=$$?; \
		docker-compose -f test/docker-compose-postgres.yml -f test/docker-compose-kafka.yml down; \
		exit $$ret

build-server: build-plugins
	go build -ldflags "-X 'github.com/conduitio/conduit/pkg/conduit.Version=${VERSION}'" -o conduit ./cmd/conduit/main.go
	@echo "build version: ${VERSION}"

build-plugins: build-file-plugin build-pg-plugin build-s3-plugin build-kafka-plugin build-generator-plugin

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
	@rm -f pkg/plugins/file/file
	@rm -f pkg/plugins/pg/pg
	@rm -f pkg/plugins/s3/s3
	@rm -f pkg/plugins/kafka/kafka
	@rm -f pkg/plugins/generator/generator

download:
	@echo Download go.mod dependencies
	@go mod download

install-tools: download
	@echo Installing tools from tools.go
	@go list -f '{{ join .Imports "\n" }}' tools.go | xargs -tI % go install %
	@go mod tidy

generate:
	go generate ./...

ui-%:
	@cd ui && make $*

readme-%:
	go run ./pkg/plugins/template/main.go pkg/plugins/template/readme-template.md $*

.PHONY: build-local build-noop-dest run-local run-latest run-latest-nightly print-results

# Builds a fresh Docker image, so we're not limited on the GA and nightly builds
build-local:
	@cd ../.. && docker build -t conduit:local .

plugins/conduit-connector-noop-dest:
	GOOS=linux GOARCH=amd64 CGO_ENABLED=0 go build -o plugins/conduit-connector-noop-dest noopdest/cmd/noopdest/main.go

run-local: plugins/conduit-connector-noop-dest
	./run.sh conduit:local

run-latest: plugins/conduit-connector-noop-dest
	docker pull ghcr.io/conduitio/conduit:latest
	./run.sh ghcr.io/conduitio/conduit:latest

run-latest-nightly: plugins/conduit-connector-noop-dest
	docker pull ghcr.io/conduitio/conduit:latest-nightly
	./run.sh ghcr.io/conduitio/conduit:latest-nightly

print-results:
	go run main.go

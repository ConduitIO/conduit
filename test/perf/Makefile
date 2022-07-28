.PHONY: build-local build-noop-dest run-local run-latest run-latest-nightly print-results

# Builds a fresh Docker image, so we're not limited on the GA and nightly builds
build-local: build-noop-dest
	@cd ../.. && docker build -t conduit:local .

build-noop-dest:
	CGO_ENABLED=0 go build -o plugins/conduit-connector-noop-dest noopdest/cmd/noopdest/main.go

run-local:
	./run.sh conduit:local

run-latest:
	./run.sh ghcr.io/conduitio/conduit:latest

run-latest-nightly:
	./run.sh ghcr.io/conduitio/conduit:latest-nightly

print-results:
	go run main.go

.PHONY: build-local build-noop-dest stop run-local run-latest run-latest-nightly print-results

# Builds a fresh Docker image, so we're not limited on the GA and nightly builds
build-local: build-noop-dest
	@cd ../.. && docker build -t conduit:local .

build-noop-dest:
	CGO_ENABLED=0 go build -o plugins/conduit-connector-noop-dest noopdest/cmd/noopdest/main.go

stop:
	docker stop conduit-perf-test || true

run-local: stop
	docker run --rm  --name conduit-perf-test -v "$(PWD)"/plugins:/plugins -p 8080:8080 -d conduit:local
	sleep 1

run-latest: stop
	docker run --rm  --name conduit-perf-test -v "$(PWD)"/plugins:/plugins -p 8080:8080 -d ghcr.io/conduitio/conduit:latest
	sleep 1

run-latest-nightly: stop
	docker run --rm  --name conduit-perf-test -v "$(PWD)"/plugins:/plugins -p 8080:8080 -d ghcr.io/conduitio/conduit:latest-nightly
	sleep 1

print-results:
	go run main.go

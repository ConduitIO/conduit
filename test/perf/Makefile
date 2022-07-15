.PHONY: build-local run-local

# Builds a fresh Docker image, so we're not limited on the GA and nightly builds
build-local:
	@cd ../.. && docker build -t conduit:local .

stop:
	docker stop conduit-perf-test || true

# todo make it possible to specify the test pipeline without changing this Makefile
run-local: stop
	docker run --rm  --name conduit-perf-test -p 8080:8080 -d conduit:local
	sleep 1
	./small-messages-low-rate.sh

run-latest: stop
	docker run --rm  --name conduit-perf-test -p 8080:8080 -d ghcr.io/conduitio/conduit:latest
	sleep 1
	./small-messages-low-rate.sh

print-results:
	go run print_results.go

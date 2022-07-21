.PHONY: build-local stop run-local run-latest print-results

# Builds a fresh Docker image, so we're not limited on the GA and nightly builds
build-local:
	@cd ../.. && docker build -t conduit:local .

stop:
	docker stop conduit-perf-test || true

run-local: stop
	docker run --rm  --name conduit-perf-test -p 8080:8080 -d conduit:local
	sleep 1

run-latest: stop
	docker run --rm  --name conduit-perf-test -p 8080:8080 -d ghcr.io/conduitio/conduit:latest
	sleep 1

print-results:
	go run print_results.go

.PHONY: build-local run-local

build-local:
	@cd ../.. && docker build -t conduit:local .

stop:
	docker stop conduit-perf-test || true

run-local: stop build-local
	docker run --rm  --name conduit-perf-test -p 8080:8080 -d conduit:local
	sleep 1
	./small-messages-high-rate.sh

run-latest: stop
	docker run --rm  --name conduit-perf-test -p 8080:8080 -d ghcr.io/conduitio/conduit:latest
	sleep 1
	./small-messages-high-rate.sh

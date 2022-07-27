## Performance benchmarks

### Principles

Our performance benchmarks is built upon the following principles:

1. It should be possible to track performance of Conduit itself (i.e. without connectors included).
2. It should be possible to track performance of Conduit with the connectors included.
3. Benchmarks are run on-demand (automated benchmarks are planned for later).
4. It's easy to manage workloads.

### Running a benchmarks

The steps are:

#### Build Conduit (optional)

If you want to run the performance benchmarks against changes which haven't been merged yet (and for which there's no
official Docker image), you can build a Docker image locally. You can do that by running `make build-local`. This will
build an image called `conduit:local`.

#### Run Conduit

Conduit can be run in any way. The only requirement is that the port 8080 is accessible. The preferred way is to run
Conduit as a Docker image. That makes it easier to get one of the official or nightly builds, and it also makes it
possible
to assign resources to the container.

Following `make` targets are provided out of the box:

1. `run-local`: uses the `conduit:local` image, which was built from the code as it is.
2. `run-latest`: uses the latest official image.
3. `run-latest-nightly`: uses the latest nightly build

#### Run a workload

Workloads are places in the [workloads](./workloads) directory and are currently in the form of bash scripts, which
create a test pipeline using Conduit's HTTP API.

#### Monitor results

This can be done using `make print-results` or [prom-graf](https://github.com/conduitio-labs/prom-graf). The differences
between the two are:

1. `make print-results` prints metrics related to records while they are in Conduit only. The Grafana dashboard show
   overall pipeline metrics.
2. The Grafana dashboard also shows Go runtime metrics, while the make target doesn't.

The test result tool has the following configuration parameters:

| parameter  | description                                                      | possible values                                             | default |
|------------|------------------------------------------------------------------|-------------------------------------------------------------|---------|
| --interval | interval at which metrics are periodically collected and printed | [Go duration string](https://pkg.go.dev/time#ParseDuration) | 10s     |
| --duration | overall duration for which the metrics will be collected         | [Go duration string](https://pkg.go.dev/time#ParseDuration) | 10m     |
| --print-to | specifies where the metrics will be printed                      | csv, console                                                | console |


| total records | rec/s (Conduit) | ms/record (Conduit) | rec/s (pipeline) | bytes/s | measuredAt |
|---------------|-----------------|---------------------|------------------|---------|------------|
| %v            | %v              | %v                  | %v               | %v      | %v         |
## Performance benchmarks

### Principles

Our performance benchmarks is built upon the following principles:

1. It should be possible to track performance of Conduit itself (i.e. without connectors included).
2. It should be possible to track performance of Conduit with the connectors included.
3. Benchmarks are run on-demand (automated benchmarks are planned for later).
4. It's easy to manage workloads.

### Running benchmark

The steps are:

#### Build Conduit (optional)

If you want to run the performance benchmarks against changes which haven't been merged yet (and for which there's no
official Docker image), you can build a Docker image locally. You can do that by running `make build-local`. This will
build an image called `conduit:local`.

#### Run a benchmark

Benchmarks are run using `make` targets which start with `run-`. Each of them:
1. Starts Conduit (the version depends on the make target)
2. Runa all workload
3. Prints results

The available `make` targets for this purpose are:
1. `run-local`: uses the `conduit:local` image, which was built from the code as it is.
2. `run-latest`: uses the latest official image.
3. `run-latest-nightly`: uses the latest nightly build

### Workloads

Workloads are pipelines configured in specific ways and exercising different areas of Conduit. For example, one workload 
can test how does Conduit behave when records with large payloads flow through it. Another one can test Conduit's with
built-in vs. standalone plugins.

Workloads are specified through bash scripts, which create and configure pipelines using Conduit's HTTP API. They need 
to be placed in the [workloads](./workloads) directory so that they are automatically run. By default, all workload 
scripts found in the `workloads` directory will be run for each performance test run.

### Printing results

The `make run-` targets use a [CLI app](main.go) to print results of a performance test. The tool can be used independently
of the `make run-` targets and has the following configuration parameters:

| parameter  | description                                                                                                       | possible values                                             | default |
|------------|-------------------------------------------------------------------------------------------------------------------|-------------------------------------------------------------|---------|
| --interval | Interval at which metrics are periodically collected and printed.                                                 | [Go duration string](https://pkg.go.dev/time#ParseDuration) | 5m      |
| --duration | Overall duration for which the metrics will be collected.                                                         | [Go duration string](https://pkg.go.dev/time#ParseDuration) | 5m      |
| --print-to | Specifies where the metrics will be printed. If 'csv'<br/> is selected, a CSV file will be automatically created. | csv, console                                                | csv     |
| --workload | Workload description.                                                                                             | any string                                                  | none    |

Another option to monitor Conduit's performance while running a performance test is [prom-graf](https://github.com/conduitio-labs/prom-graf). 
The differences between the CLI tool and `prom-graf` are:

1. The CLI tool prints metrics related to records while they are in Conduit only. The Grafana dashboard show
   overall pipeline metrics.
2. The Grafana dashboard also shows Go runtime metrics, while the make target doesn't.

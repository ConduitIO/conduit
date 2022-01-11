### General
The generator plugin is able to generate sample records with its source.
It has no destination and trying to use it will result in an error.

The data is generated in JSON format. The JSON objects themselves are generated using a field specification, which is 
explained in more details in the [Configuration section](#Configuration) below.

The plugin is great for getting started with Conduit but also for certain types of performance tests.

### How to build?
Run `make build-generator-plugin`.

### Testing
Run `make test` to run all the unit tests.

### Configuration
`recordCount`
The number of records to be generated.
If a negative value is used, the source will generate records until stopped.

`readTime`
The time it takes to 'read' a record. It can be used to simulate latency and, for example, simulate slow or fast sources.

`fields`
A comma-separated list of name:type tokens, where type can be: int, string, time, bool. An example is: `id:int,name:string,joined:time,admin:bool`.

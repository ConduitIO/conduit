# Conduit API Proto files

This folder contains protobuf files that define the Conduit gRPC API and
consequently also the HTTP API via
the [grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway). The proto
schema is uploaded to
the [Buf schema registry](https://docs.buf.build/bsr/introduction) and can be
found here: https://buf.build/conduitio/conduit

Buf schema registry provides
[remote code generation](https://docs.buf.build/bsr/remote-generation/go)
which we use in Conduit to get the Go code for our gRPC server. We recommend
you doing the same if you are trying to communicate with Conduit's gRPC API.

To fetch the remote generated code for Go you can use:

```
go get go.buf.build/conduitio/conduit/conduitio/conduit@latest
```

## Local development

Because we use remote code generation provided by the Buf schema registry there
is no locally generated code. When developing locally we don't want to push a
new version of the proto files every time we make a change, that's why in that
case we can switch to locally generated protobuf code.

To switch to locally generated protobuf code follow the following steps:
- run `cd proto && buf generate`
- cd into the newly generated folder `proto/gen`
- create a `go.mod` file by running `go mod init go.buf.build/conduitio/conduit/conduitio/conduit && go mod tidy`
- cd into the root of the project and run `go mod edit -replace go.buf.build/conduitio/conduit/conduitio/conduit=./proto/gen && go mod tidy`

Before you push your changes don't forget to revert the replaced dependency
using `go mod edit -dropreplace go.buf.build/conduitio/conduit/conduitio/conduit`.

Because Conduit depends on the remotely generated code you will normally have
to create 2 separate PRs - the first changes the proto files and uploads a
new version of the schema to the schema registry once it's merged, the second
updates the dependency to `go.buf.build/conduitio/conduit/conduitio/conduit`
and includes any necessary Go code changes. The switch to locally generated
code can help you prepare and test both parts at once.

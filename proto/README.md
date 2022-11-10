# Conduit API Proto files

This folder contains protobuf files that define the Conduit gRPC API and
consequently also the HTTP API via
the [grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway).

## Development

We use [Buf](https://buf.build/) to generate the Go code. The code is locally generated,
and can be found in [gen](/proto/gen). The generated code needs to be committed.

The code needs to be generated after changes to the `.proto` files have been made. To do
so run `make proto-generate` from the root of this repository.

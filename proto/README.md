# Conduit API Proto files

This folder contains protobuf files that define the Conduit gRPC API and
consequently also the HTTP API via
the [grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway).

## Client code

The client code for Conduit's API is available as a [Buf remote package](https://docs.buf.build/bsr/remote-packages/go).
Proto files are pushed to the Buf Schema Registry via a GitHub workflow defined [here](/.github/workflows/buf-validate.yaml).

To use the client code, firstly run:

```shell
go get buf.build/gen/go/conduitio/conduit/grpc/go
```

Here's an example usage of Conduit's client code:

```go
package main

import (
	"context"

	"buf.build/gen/go/conduitio/conduit/grpc/go/api/v1/apiv1grpc"
	apiv1 "buf.build/gen/go/conduitio/conduit/protocolbuffers/go/api/v1"
	"google.golang.org/grpc"
)

func main() {
	var cc grpc.ClientConnInterface = ...
	ps := apiv1grpc.NewPipelineServiceClient(cc)
	pipeline, err := ps.GetPipeline(
		context.Background(),
		&apiv1.GetPipelineRequest{Id: "pipeline-id-here"},
	)
}
```

## Development

We use [Buf](https://buf.build/) to generate the Go code. The code is locally generated,
and can be found in [/proto/api/v1](/proto/api/v1). The generated code needs to be committed.

The code needs to be generated after changes to the `.proto` files have been made. To do
so run `make proto-generate` from the root of this repository.

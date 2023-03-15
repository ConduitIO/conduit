# Conduit API Proto files

This folder contains protobuf files that define the Conduit gRPC API and
consequently also the HTTP API via
the [grpc-gateway](https://github.com/grpc-ecosystem/grpc-gateway).

## Client code

The client code for Conduit's API is available remotely generated via
[Buf's Remote Generation](https://docs.buf.build/bsr/remote-generation/overview). Remote code generation is triggered
via a GitHub workflow defined [here](/.github/workflows/buf.yml).

To use the client code, firstly run:

```shell
go get buf.build/gen/go/conduitio/conduit/grpc/go
```

Here's an example usage of Conduit's client code:

```go
package main

import (
	"context"
	apiv1 "go.buf.build/conduitio/conduit/conduitio/conduit/api/v1"
	"google.golang.org/grpc"
)

func main() {
	var cc grpc.ClientConnInterface = ...
	ps := apiv1.NewPipelineServiceClient(cc)
	pipeline, err := ps.GetPipeline(
		context.Background(),
		&apiv1.GetPipelineRequest{Id: "pipeline-id-here"},
	)
}

```

## Development

We use [Buf](https://buf.build/) to generate the Go code. The code is locally generated,
and can be found in [gen](/proto/gen). The generated code needs to be committed.

The code needs to be generated after changes to the `.proto` files have been made. To do
so run `make proto-generate` from the root of this repository.

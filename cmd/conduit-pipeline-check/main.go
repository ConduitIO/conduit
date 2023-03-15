// Copyright © 2022 Meroxa, Inc.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"os/signal"
	"time"

	apiv1 "github.com/conduitio/conduit/proto/api/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials/insecure"
)

const (
	exitCodeErr       = 1
	exitCodeInterrupt = 2
)

func main() {
	ctx := cancelOnInterrupt(context.Background())

	if err := checkPipeline(ctx); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(exitCodeErr)
	}
}

// checkPipeline checks the pipeline running status.
// error is returned when an unexpected error is encountered.
// * missing or invalid flags
// * gRPC connection failures
// * operation takes too long to execute
func checkPipeline(ctx context.Context) error {
	flags := flag.NewFlagSet(os.Args[0], flag.ExitOnError)
	var (
		grpcAddress  = flags.String("grpc.address", ":8084", "address of the Conduit server gRPC API")
		pipelineName = flags.String("pipeline.name", "", "name of the target pipeline")
		timeout      = flags.Duration("timeout", 10*time.Second, "timeout duration")
		verbose      = flags.Bool("verbose", false, "print additional information during execution")
	)

	_ = flags.Parse(os.Args[1:])

	if pipelineName == nil || *pipelineName == "" {
		return fmt.Errorf("pipeline.name is not set")
	}

	opts := []grpc.DialOption{
		grpc.WithTransportCredentials(insecure.NewCredentials()),
	}

	dialCtx, dialCancel := context.WithTimeout(ctx, *timeout)
	defer dialCancel()

	c, err := grpc.DialContext(dialCtx, *grpcAddress, opts...)
	if err != nil {
		return fmt.Errorf("failed to connect to conduit grpc server: %w", err)
	}
	defer c.Close()

	pipeline := apiv1.NewPipelineServiceClient(c)
	p, err := pipeline.GetPipeline(
		ctx,
		&apiv1.GetPipelineRequest{
			Id: *pipelineName,
		})
	if err != nil {
		return fmt.Errorf("failed to find pipeline %q: %w", *pipelineName, err)
	}

	switch p.Pipeline.State.Status {
	case apiv1.Pipeline_STATUS_RUNNING:
		if *verbose {
			fmt.Printf("pipeline %q is running", *pipelineName)
		}
		// success
		return nil
	default:
		return fmt.Errorf("pipeline %q is not running: %v", *pipelineName, p.Pipeline.State.Status)
	}
}

// cancelOnInterrupt returns a context that is canceled when the interrupt
// signal is received.
// * After the first signal the function will continue to listen
// * On the second signal executes a hard exit, without waiting for a graceful
// shutdown.
func cancelOnInterrupt(ctx context.Context) context.Context {
	ctx, cancel := context.WithCancel(ctx)
	signalChan := make(chan os.Signal, 1)
	signal.Notify(signalChan, os.Interrupt)
	go func() {
		select {
		case <-signalChan: // first interrupt signal
			cancel()
		case <-ctx.Done():
		}
		<-signalChan // second interrupt signal
		os.Exit(exitCodeInterrupt)
	}()
	return ctx
}

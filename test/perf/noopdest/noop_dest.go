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

package noopdest

import (
	"context"
	sdk "github.com/conduitio/conduit-connector-sdk"
)

func Spec() sdk.Specification {
	return sdk.Specification{
		Name:              "NoOp destination",
		Summary:           "A NoOp destination connector, simply ack-ing all records.",
		Description:       "A NoOp destination connector, simply ack-ing all records.",
		Version:           "v0.1.0",
		Author:            "Meroxa, Inc.",
		DestinationParams: map[string]sdk.Parameter{},
		SourceParams:      map[string]sdk.Parameter{},
	}
}

type destination struct {
	sdk.UnimplementedDestination
}

func NewDestination() sdk.Destination {
	return destination{}
}

func (d destination) Configure(_ context.Context, _ map[string]string) error {
	return nil
}

func (d destination) Open(ctx context.Context) error {
	return nil
}

func (d destination) WriteAsync(_ context.Context, _ sdk.Record, ackFunc sdk.AckFunc) error {
	return ackFunc(nil)
}

func (d destination) Flush(_ context.Context) error {
	return nil
}

func (d destination) Teardown(_ context.Context) error {
	return nil
}

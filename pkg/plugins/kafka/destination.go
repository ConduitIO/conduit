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

package kafka

import (
	"context"
	"fmt"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
)

type Destination struct {
	sdk.UnimplementedDestination
	
	Client Producer
	Config Config
}

func NewDestination() sdk.Destination {
	return nil
}

func (d *Destination) Configure(ctx context.Context, cfg map[string]string) error {
	fmt.Println("Configuring a Kafka Destination...")
	parsed, err := Parse(cfg)
	if err != nil {
		return cerrors.Errorf("config is invalid: %w", err)
	}
	d.Config = parsed
	return nil
}

func (d *Destination) Open(ctx context.Context) error {
	client, err := NewProducer(d.Config)
	if err != nil {
		return cerrors.Errorf("failed to create Kafka client: %w", err)
	}

	d.Client = client
	return nil
}

func (d *Destination) Write(ctx context.Context, record sdk.Record) error {
	err := d.Client.Send(
		record.Key.Bytes(),
		record.Payload.Bytes(),
	)
	if err != nil {
		return cerrors.Errorf("message not delivered %w", err)
	}
	return nil
}

func (d *Destination) Flush(context.Context) error {
	return nil
}

// Teardown shuts down the Kafka client.
func (d *Destination) Teardown(context.Context) error {
	fmt.Println("Tearing down a Kafka Destination...")
	d.Client.Close()
	return nil
}

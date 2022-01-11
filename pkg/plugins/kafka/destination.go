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
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"
)

type Destination struct {
	Client Producer
	Config Config
}

func (s *Destination) Open(ctx context.Context, cfg plugins.Config) error {
	fmt.Println("Opening a Kafka Destination...")
	parsed, err := Parse(cfg.Settings)
	if err != nil {
		return cerrors.Errorf("config is invalid: %w", err)
	}
	s.Config = parsed

	client, err := NewProducer(s.Config)
	if err != nil {
		return cerrors.Errorf("failed to create Kafka client: %w", err)
	}

	s.Client = client
	return nil
}

func (s *Destination) Write(ctx context.Context, record record.Record) (record.Position, error) {
	err := s.Client.Send(
		record.Key.Bytes(),
		record.Payload.Bytes(),
	)
	if err != nil {
		return nil, cerrors.Errorf("message not delivered %w", err)
	}
	return record.Position, nil
}

// Teardown shuts down the Kafka client.
func (s *Destination) Teardown() error {
	fmt.Println("Tearing down a Kafka Destination...")
	s.Client.Close()
	return nil
}

// Validate takes config and returns an error if some values are missing or incorrect.
func (s *Destination) Validate(cfg plugins.Config) error {
	_, err := Parse(cfg.Settings)
	return err
}

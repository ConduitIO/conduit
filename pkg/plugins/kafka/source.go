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
	"bytes"
	"context"
	"fmt"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/segmentio/kafka-go"
)

type Source struct {
	sdk.UnimplementedSource

	Consumer         Consumer
	Config           Config
	lastPositionRead sdk.Position
}

func NewSource() sdk.Source {
	return &Source{}
}

func (s *Source) Configure(ctx context.Context, cfg map[string]string) error {
	fmt.Println("Configuring a Kafka Source...")
	parsed, err := Parse(cfg)
	if err != nil {
		return cerrors.Errorf("config is invalid: %w", err)
	}
	s.Config = parsed
	return nil
}

func (s *Source) Open(ctx context.Context, pos sdk.Position) error {
	client, err := NewConsumer()
	if err != nil {
		return cerrors.Errorf("failed to create Kafka client: %w", err)
	}
	s.Consumer = client

	err = s.startFrom(pos)
	if err != nil {
		return cerrors.Errorf("couldn't start from position: %w", err)
	}

	return nil
}

func (s *Source) Read(ctx context.Context) (sdk.Record, error) {
	message, kafkaPos, err := s.Consumer.Get(ctx)
	if err != nil {
		return sdk.Record{}, cerrors.Errorf("failed getting a message %w", err)
	}
	if message == nil {
		return sdk.Record{}, plugins.ErrEndData
	}
	rec, err := toRecord(message, kafkaPos)
	if err != nil {
		return sdk.Record{}, cerrors.Errorf("couldn't transform record %w", err)
	}
	s.lastPositionRead = rec.Position
	return rec, nil
}

func (s *Source) startFrom(position sdk.Position) error {
	// The check is in place, to avoid reconstructing the Kafka consumer.
	if s.lastPositionRead != nil && bytes.Equal(s.lastPositionRead, position) {
		return nil
	}

	err := s.Consumer.StartFrom(s.Config, string(position))
	if err != nil {
		return cerrors.Errorf("couldn't start from given position %v due to %w", string(position), err)
	}
	s.lastPositionRead = position
	return nil
}

func toRecord(message *kafka.Message, position string) (sdk.Record, error) {
	return sdk.Record{
		Position:  []byte(position),
		CreatedAt: time.Time{},
		Key:       sdk.RawData(message.Key),
		Payload:   sdk.RawData(message.Key),
	}, nil
}

func (s *Source) Ack(ctx context.Context, position sdk.Position) error {
	return s.Consumer.Ack()
}

func (s *Source) Teardown(context.Context) error {
	fmt.Println("Tearing down a Kafka Source...")
	s.Consumer.Close()
	return nil
}

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
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/segmentio/kafka-go"
)

type Source struct {
	Consumer         Consumer
	Config           Config
	lastPositionRead record.Position
}

func (s *Source) Open(ctx context.Context, cfg plugins.Config) error {
	fmt.Println("Opening a Kafka Source...")
	parsed, err := Parse(cfg.Settings)
	if err != nil {
		return cerrors.Errorf("config is invalid: %w", err)
	}
	s.Config = parsed

	client, err := NewConsumer()
	if err != nil {
		return cerrors.Errorf("failed to create Kafka client: %w", err)
	}

	s.Consumer = client
	return nil
}

func (s *Source) Teardown() error {
	fmt.Println("Tearing down a Kafka Source...")
	s.Consumer.Close()
	return nil
}

func (s *Source) Validate(cfg plugins.Config) error {
	_, err := Parse(cfg.Settings)
	return err
}

func (s *Source) Read(ctx context.Context, position record.Position) (record.Record, error) {
	err := s.startFrom(position)
	if err != nil {
		return record.Record{}, cerrors.Errorf("couldn't start from position: %w", err)
	}

	message, kafkaPos, err := s.Consumer.Get(ctx)
	if err != nil {
		return record.Record{}, cerrors.Errorf("failed getting a message %w", err)
	}
	if message == nil {
		return record.Record{}, plugins.ErrEndData
	}
	rec, err := toRecord(message, kafkaPos)
	if err != nil {
		return record.Record{}, cerrors.Errorf("couldn't transform record %w", err)
	}
	s.lastPositionRead = rec.Position
	return rec, nil
}

func (s *Source) startFrom(position record.Position) error {
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

func toRecord(message *kafka.Message, position string) (record.Record, error) {
	return record.Record{
		Position:  []byte(position),
		CreatedAt: time.Time{},
		ReadAt:    time.Time{},
		Key:       record.RawData{Raw: message.Key},
		Payload:   record.RawData{Raw: message.Value},
	}, nil
}

func (s *Source) Ack(context.Context, record.Position) error {
	return s.Consumer.Ack()
}

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

//go:generate mockgen -destination mock/producer.go -package mock -mock_names=Producer=Producer . Producer

package kafka

import (
	"context"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/segmentio/kafka-go"
)

type Producer interface {
	// Send synchronously delivers a message.
	// Returns an error, if the message could not be delivered.
	Send(key []byte, payload []byte) error

	// Close this producer and the associated resources (e.g. connections to the broker)
	Close()
}

type segmentProducer struct {
	writer *kafka.Writer
}

// NewProducer creates a new Kafka producer.
// The current implementation uses Segment's kafka-go client.
func NewProducer(config Config) (Producer, error) {
	if config.Servers == "" {
		return nil, ErrServersMissing
	}
	if config.Topic == "" {
		return nil, ErrTopicMissing
	}
	servers, err := split(config.Servers)
	if err != nil {
		return nil, cerrors.Errorf("invalid servers: %w", err)
	}
	writer := &kafka.Writer{
		Addr:         kafka.TCP(servers...),
		Topic:        config.Topic,
		BatchSize:    1,
		WriteTimeout: config.DeliveryTimeout,
		RequiredAcks: config.Acks,
		MaxAttempts:  3,
		// todo use a secure transport
		// Transport: nil,
	}
	return &segmentProducer{writer: writer}, nil
}

func split(serversString string) ([]string, error) {
	split := strings.Split(serversString, ",")
	servers := make([]string, 0)
	for i, s := range split {
		if strings.Trim(s, " ") == "" {
			return nil, cerrors.Errorf("empty %d. server", i)
		}
		servers = append(servers, s)
	}
	return servers, nil
}

func (c *segmentProducer) Send(key []byte, payload []byte) error {
	err := c.writer.WriteMessages(
		context.Background(),
		kafka.Message{
			Key:   key,
			Value: payload,
		},
	)

	if err != nil {
		return cerrors.Errorf("message not delivered: %w", err)
	}
	return nil
}

func (c *segmentProducer) Close() {
	if c.writer != nil {
		c.writer.Close()
	}
}

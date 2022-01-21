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

//go:build integration

package kafka_test

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/plugins/kafka"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/google/uuid"
	skafka "github.com/segmentio/kafka-go"
)

// todo try optimizing, the test takes 15 seconds to run!
func TestDestination_Write_Simple(t *testing.T) {
	// prepare test data
	cfg := newTestConfig(t)
	createTopic(t, cfg.Settings[kafka.Topic])
	record := testRecord()

	// prepare SUT
	underTest := kafka.Destination{}
	openErr := underTest.Open(context.Background(), cfg)
	defer underTest.Teardown()
	assert.Ok(t, openErr)

	// act and assert
	result, writeErr := underTest.Write(context.Background(), record)
	assert.Ok(t, writeErr)
	assert.Equal(t, record.Position, result)

	// todo wait at most a certain amount of time
	message, err := waitForReaderMessage(cfg.Settings[kafka.Topic], 4000*time.Millisecond)
	assert.Ok(t, err)
	assert.Equal(t, record.Payload.Bytes(), message.Value)
}

func waitForReaderMessage(topic string, timeout time.Duration) (skafka.Message, error) {
	// Kafka is started in Docker
	reader := newKafkaReader(topic)
	defer reader.Close()

	withTimeout, _ := context.WithTimeout(context.Background(), timeout)
	return reader.ReadMessage(withTimeout)
}

func newTestConfig(t *testing.T) plugins.Config {
	return plugins.Config{Settings: map[string]string{
		kafka.Servers: "localhost:9092",
		kafka.Topic:   t.Name() + uuid.NewString(),
	}}
}

func testRecord() record.Record {
	return record.Record{
		Position:  []byte(uuid.NewString()),
		Metadata:  nil,
		CreatedAt: time.Time{},
		ReadAt:    time.Time{},
		Key:       record.RawData{Raw: []byte(uuid.NewString())},
		Payload:   record.RawData{Raw: []byte(fmt.Sprintf("test message %s", time.Now()))},
	}
}

func newKafkaReader(topic string) (reader *skafka.Reader) {
	return skafka.NewReader(skafka.ReaderConfig{
		Brokers:     []string{"localhost:9092"},
		Topic:       topic,
		StartOffset: skafka.FirstOffset,
		GroupID:     uuid.NewString(),
	})
}

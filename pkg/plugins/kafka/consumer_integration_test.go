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

package kafka

import (
	"context"
	"fmt"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/confluentinc/confluent-kafka-go/kafka"
	"github.com/google/uuid"
)

func TestConfluentClient_StartFrom_EmptyPosition(t *testing.T) {
	t.Parallel()

	cfg := Config{Topic: "TestConfluentClient_" + uuid.NewString(), Servers: "localhost:9092"}
	createTopic(t, cfg, 1)

	consumer, err := NewConsumer(cfg)
	assert.Ok(t, err)

	err = consumer.StartFrom(cfg.Topic, map[int32]int64{}, true)
	defer consumer.Close()
	assert.Ok(t, err)
}

func TestConfluentClient_StartFrom_FromBeginning(t *testing.T) {
	t.Parallel()

	cfg := Config{
		Topic:             "TestConfluentClient_" + uuid.NewString(),
		Servers:           "localhost:9092",
		ReadFromBeginning: true,
	}
	// other two partitions should be consumed from beginning
	positions := map[int32]int64{0: 1}

	partitions := 3
	createTopic(t, cfg, partitions)

	sendTestMessages(t, cfg, partitions)

	consumer, err := NewConsumer(cfg)
	defer consumer.Close()
	assert.Ok(t, err)

	err = consumer.StartFrom(cfg.Topic, positions, cfg.ReadFromBeginning)
	assert.Ok(t, err)

	// 1 message from first partition
	// +4 messages from 2 partitions which need to be read fully
	messagesUnseen := map[string]bool{
		"test-key-1": true,
		"test-key-2": true,
		"test-key-4": true,
		"test-key-5": true,
		"test-key-6": true,
	}
	for i := 1; i <= 5; i++ {
		message, _, err := consumer.Get(msgTimeout)
		assert.NotNil(t, message)
		assert.Ok(t, err)
		delete(messagesUnseen, string(message.Key))
	}
	assert.Equal(t, 0, len(messagesUnseen))

	message, updatedPos, err := consumer.Get(msgTimeout)
	assert.Ok(t, err)
	assert.Nil(t, message)
	assert.Equal(
		t,
		map[int32]int64{0: 2, 1: 2, 2: 2},
		updatedPos,
	)
}

func TestConfluentClient_StartFrom(t *testing.T) {
	cases := []struct {
		name      string
		cfg       Config
		positions map[int32]int64
	}{
		{
			name: "StartFrom: Only new",
			cfg: Config{
				Topic:             "TestConfluentClient_" + uuid.NewString(),
				Servers:           "localhost:9092",
				ReadFromBeginning: false,
			},
			positions: map[int32]int64{0: 1},
		},
		{
			name: "StartFrom: Simple test",
			cfg: Config{
				Topic:   "TestConfluentClient_" + uuid.NewString(),
				Servers: "localhost:9092",
			},
			positions: map[int32]int64{0: 1, 1: 2, 2: 2},
		},
	}

	for _, tt := range cases {
		// https://github.com/golang/go/wiki/CommonMistakes#using-goroutines-on-loop-iterator-variables
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			testConfluentClient_StartFrom(t, tt.cfg, tt.positions)
		})
	}
}

func testConfluentClient_StartFrom(t *testing.T, cfg Config, positions map[int32]int64) {
	partitions := 3
	createTopic(t, cfg, partitions)

	sendTestMessages(t, cfg, partitions)

	consumer, err := NewConsumer(cfg)
	defer consumer.Close()
	assert.Ok(t, err)

	err = consumer.StartFrom(cfg.Topic, positions, cfg.ReadFromBeginning)
	assert.Ok(t, err)

	message, _, err := consumer.Get(msgTimeout)
	assert.NotNil(t, message)
	assert.Ok(t, err)
	assert.Equal(t, "test-key-6", string(message.Key))
	assert.Equal(t, "test-payload-6", string(message.Value))

	message, updatedPos, err := consumer.Get(msgTimeout)
	assert.Ok(t, err)
	assert.Nil(t, message)
	assert.Equal(
		t,
		map[int32]int64{0: 2, 1: 2, 2: 2},
		updatedPos,
	)
}

// partition 0 has messages: 3 and 6
// partition 1 has messages: 1 and 4
// partition 2 has messages: 2 and 5
func sendTestMessages(t *testing.T, cfg Config, partitions int) {
	producer, err := kafka.NewProducer(cfg.AsKafkaCfg())
	defer producer.Close()
	assert.Ok(t, err)

	for i := 1; i <= 6; i++ {
		err = sendTestMessage(
			producer,
			cfg.Topic,
			fmt.Sprintf("test-key-%d", i),
			fmt.Sprintf("test-payload-%d", i),
			int32(i%partitions),
		)
		assert.Ok(t, err)
	}
	unflushed := producer.Flush(5000)
	assert.Equal(t, 0, unflushed)
}

func sendTestMessage(producer *kafka.Producer, topic string, key string, payload string, partition int32) error {
	return producer.Produce(
		&kafka.Message{
			Key:            []byte(key),
			TopicPartition: kafka.TopicPartition{Topic: &topic, Partition: partition},
			Value:          []byte(payload),
		},
		make(chan kafka.Event, 10),
	)
}

func TestGet_KafkaDown(t *testing.T) {
	t.Parallel()

	cfg := Config{Topic: "client_integration_test_topic", Servers: "localhost:12345"}
	consumer, err := NewConsumer(cfg)
	assert.Ok(t, err)

	err = consumer.StartFrom(cfg.Topic, map[int32]int64{0: 123}, true)
	assert.Error(t, err)
	var kerr kafka.Error
	if !cerrors.As(err, &kerr) {
		t.Fatal("expected kafka.Error")
	}
	assert.Equal(t, kafka.ErrTransport, kerr.Code())
}

func createTopic(t *testing.T, cfg Config, partitions int) {
	kafkaCfg := cfg.AsKafkaCfg()
	adminClient, _ := kafka.NewAdminClient(kafkaCfg)
	defer adminClient.Close()

	_, err := adminClient.CreateTopics(context.Background(), []kafka.TopicSpecification{{Topic: cfg.Topic, NumPartitions: partitions}})
	assert.Ok(t, err)
}

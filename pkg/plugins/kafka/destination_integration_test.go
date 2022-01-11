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
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/confluentinc/confluent-kafka-go/kafka"
	"github.com/google/uuid"
)

// todo try optimizing, the test takes 15 seconds to run!
func TestDestination_Write_Simple(t *testing.T) {
	// Kafka is started in Docker
	consumer := newKafkaConsumer(t)
	defer consumer.Close()

	// prepare test data
	cfg := newTestConfig(t)
	createDestinationTopic(t, consumer, cfg.Settings[Topic])
	record := testRecord()

	// prepare SUT
	underTest := Destination{}
	openErr := underTest.Open(context.Background(), cfg)
	defer underTest.Teardown()
	assert.Ok(t, openErr)

	// act and assert
	result, writeErr := underTest.Write(context.Background(), record)
	assert.Ok(t, writeErr)
	assert.Equal(t, record.Position, result)

	message := waitForMessage(consumer, 2000, cfg.Settings[Topic])
	assert.NotNil(t, message)
	assert.Equal(t, record.Payload.Bytes(), message.Value)
}

func newTestConfig(t *testing.T) plugins.Config {
	return plugins.Config{Settings: map[string]string{
		Servers: "localhost:9092",
		Topic:   t.Name() + uuid.NewString(),
	}}
}

func waitForMessage(consumer *kafka.Consumer, timeoutMs int, topic string) *kafka.Message {
	consumer.SubscribeTopics([]string{topic}, nil)

	var message *kafka.Message
	waited := 0
	for waited < timeoutMs && message == nil {
		event := consumer.Poll(100)
		messageMaybe, ok := event.(*kafka.Message)
		if ok {
			message = messageMaybe
		}
	}
	return message
}

func createDestinationTopic(t *testing.T, consumer *kafka.Consumer, topic string) {
	adminClient, _ := kafka.NewAdminClientFromConsumer(consumer)
	defer adminClient.Close()

	_, err := adminClient.CreateTopics(
		context.Background(),
		[]kafka.TopicSpecification{{Topic: topic, NumPartitions: 1}},
	)
	assert.Ok(t, err)
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

func newKafkaConsumer(t *testing.T) (consumer *kafka.Consumer) {
	consumer, err := kafka.NewConsumer(&kafka.ConfigMap{
		"bootstrap.servers":  "localhost:9092",
		"auto.offset.reset":  "earliest",
		"enable.auto.commit": false,
		"group.id":           "None"})

	if err != nil {
		t.Fatalf("Failed to create consumer: %s\n", err)
	}
	return
}

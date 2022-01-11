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

//go:generate mockgen -destination mock/consumer.go -package mock -mock_names=Consumer=Consumer . Consumer

package kafka

import (
	"fmt"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/confluentinc/confluent-kafka-go/kafka"
)

type Consumer interface {
	// Get returns a message from the configured topic, waiting at most 'timeoutMs' milliseconds.
	// Returns:
	// A message and the client's 'position' in Kafka, if there's no error, OR
	// A nil message, the client's position in Kafka, and a nil error,
	// if no message was retrieved within the specified timeout, OR
	// A nil message, nil position and an error if there was an error while retrieving the message (e.g. broker down).
	Get(timeout time.Duration) (*kafka.Message, map[int32]int64, error)

	// Close this consumer and the associated resources (e.g. connections to the broker)
	Close()

	// StartFrom reads messages from the given topic, starting from the given positions.
	// For new partitions or partitions not found in the 'position',
	// the reading behavior is specified by 'readFromBeginning' parameter:
	// if 'true', then all messages will be read, if 'false', only new messages will be read.
	// Returns: An error, if the consumer could not be set to read from the given position, nil otherwise.
	StartFrom(topic string, position map[int32]int64, readFromBeginning bool) error
}

type confluentConsumer struct {
	Consumer  *kafka.Consumer
	positions map[int32]int64
}

// NewConsumer creates a new Kafka consumer.
// The current implementation uses Confluent's Kafka client.
// Full list of configuration properties is available here:
// https://github.com/edenhill/librdkafka/blob/master/CONFIGURATION.md
func NewConsumer(config Config) (Consumer, error) {
	consumer, err := kafka.NewConsumer(config.AsKafkaCfg())
	if err != nil {
		return nil, cerrors.Errorf("couldn't create consumer: %w", err)
	}
	return &confluentConsumer{Consumer: consumer, positions: map[int32]int64{}}, nil
}

func (c *confluentConsumer) Get(timeout time.Duration) (*kafka.Message, map[int32]int64, error) {
	if c.noPositions() {
		return nil, nil, cerrors.New("no positions set, call StartFrom first")
	}

	endAt := time.Now().Add(timeout)
	for timeLeft := -time.Since(endAt); timeLeft > 0; timeLeft = -time.Since(endAt) {
		event := c.Consumer.Poll(int(timeLeft.Milliseconds()))
		// there are events of other types, but we're not interested in those.
		// More info is available here:
		// https://docs.confluent.io/5.5.0/clients/confluent-kafka-go/index.html#hdr-Consumer_events
		switch v := event.(type) {
		case *kafka.Message:
			return v, c.updatePosition(v), nil
		case kafka.Error:
			return nil, nil, cerrors.Errorf("received error from client %v", v)
		}
	}
	// no message, no error
	return nil, c.updatePosition(nil), nil
}

func (c *confluentConsumer) StartFrom(topic string, position map[int32]int64, readFromBeginning bool) error {
	defaultOffsets, err := c.defaultOffsets(topic, readFromBeginning)
	if err != nil {
		return cerrors.Errorf("couldn't get default offsets: %w", err)
	}

	completePos := merge(defaultOffsets, position)
	partitions, err := toKafkaPositions(&topic, completePos)
	if err != nil {
		return cerrors.Errorf("couldn't get offsets: %w", err)
	}

	err = c.Consumer.Assign(partitions)
	if err != nil {
		return cerrors.Errorf("couldn't assign partitions: %w", err)
	}

	c.positions = completePos
	return nil
}

func (c *confluentConsumer) defaultOffsets(topic string, readFromBeginning bool) (map[int32]int64, error) {
	// to get the number of partitions
	partitions, err := c.countPartitions(topic)
	if err != nil {
		return nil, cerrors.Errorf("couldn't count partitions: %w", err)
	}
	offsets := map[int32]int64{}

	// get last offset for each partition
	for i := 0; i < partitions; i++ {
		lo, hi, err := c.Consumer.QueryWatermarkOffsets(topic, int32(i), 5000)
		if err != nil {
			return nil, cerrors.Errorf("couldn't get default offsets: %w", err)
		}
		offset := hi
		if readFromBeginning {
			offset = lo
		}
		offsets[int32(i)] = offset
	}
	return offsets, nil
}

func (c *confluentConsumer) countPartitions(topic string) (int, error) {
	metadata, err := c.Consumer.GetMetadata(&topic, false, 10000)
	if err != nil {
		return 0, cerrors.Errorf("couldn't get metadata: %w", err)
	}
	return len(metadata.Topics[topic].Partitions), nil
}

func toKafkaPositions(topic *string, position map[int32]int64) ([]kafka.TopicPartition, error) {
	partitions := make([]kafka.TopicPartition, 0, len(position))
	for k, v := range position {
		offset, err := kafka.NewOffset(v)
		if err != nil {
			return nil, cerrors.Errorf("invalid offset: %w", err)
		}
		partitions = append(partitions, kafka.TopicPartition{Topic: topic, Partition: k, Offset: offset})
	}
	return partitions, nil
}

func (c *confluentConsumer) updatePosition(msg *kafka.Message) map[int32]int64 {
	if msg == nil {
		return c.positions
	}
	c.positions[msg.TopicPartition.Partition] = c.increment(msg.TopicPartition.Offset)
	return c.positions
}

func (c *confluentConsumer) increment(offset kafka.Offset) int64 {
	switch offset {
	case kafka.OffsetBeginning, kafka.OffsetEnd, kafka.OffsetInvalid, kafka.OffsetStored:
		panic(cerrors.Errorf("got unexpected offset %v", offset))
	default:
		return int64(offset) + 1
	}
}

func (c *confluentConsumer) Close() {
	if c.Consumer == nil {
		return
	}
	err := c.Consumer.Close()
	if err != nil {
		fmt.Printf("couldn't close consumer due to error: %v\n", err)
	}
}

func (c *confluentConsumer) noPositions() bool {
	return len(c.positions) == 0
}

func merge(first map[int32]int64, second map[int32]int64) map[int32]int64 {
	merged := map[int32]int64{}
	for k, v := range first {
		merged[k] = v
	}
	for k, v := range second {
		merged[k] = v
	}
	return merged
}

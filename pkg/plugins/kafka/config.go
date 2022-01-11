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
	"strconv"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/confluentinc/confluent-kafka-go/kafka"
	"github.com/google/uuid"
	skafka "github.com/segmentio/kafka-go"
)

const (
	Servers           = "servers"
	Topic             = "topic"
	SecurityProtocol  = "securityProtocol"
	Acks              = "acks"
	DeliveryTimeout   = "deliveryTimeout"
	ReadFromBeginning = "readFromBeginning"
)

var Required = []string{Servers, Topic}

// Config contains all the possible configuration parameters for Kafka sources and destinations.
// When changing this struct, please also change the plugin specification (in main.go) as well as the ReadMe.
type Config struct {
	// A list of bootstrap servers, which will be used to discover all the servers in a cluster.
	// Maps to "bootstrap.servers" in a Kafka consumer's configuration
	Servers string
	Topic   string
	// Maps to "security.protocol" in a Kafka consumer's configuration
	SecurityProtocol string
	// Maps to "acks" in a Kafka consumer's configuration
	Acks            skafka.RequiredAcks
	DeliveryTimeout time.Duration
	// Read all messages present in a source topic.
	// Default value: false (only new messages are read)
	ReadFromBeginning bool
}

func (c Config) AsKafkaCfg() *kafka.ConfigMap {
	kafkaCfg := &kafka.ConfigMap{
		"bootstrap.servers": c.Servers,
		"group.id":          uuid.New().String(),
		// because we wan't to be able to 'seek' to specific positions in a topic
		// we need to manually manage the consumer state.
		"enable.auto.commit": false,
		"client.id":          "conduit-kafka-source",
	}

	if c.SecurityProtocol != "" {
		// nolint:errcheck // returns nil always
		kafkaCfg.SetKey("security.protocol", c.SecurityProtocol)
	}
	return kafkaCfg
}

func Parse(cfg map[string]string) (Config, error) {
	err := checkRequired(cfg)
	// todo check if values are valid, e.g. hosts are valid etc.
	if err != nil {
		return Config{}, err
	}
	var parsed = Config{
		Servers:          cfg[Servers],
		Topic:            cfg[Topic],
		SecurityProtocol: cfg[SecurityProtocol],
	}
	// parse acknowledgment setting
	ack, err := parseAcks(cfg[Acks])
	if err != nil {
		return Config{}, cerrors.Errorf("couldn't parse ack: %w", err)
	}
	parsed.Acks = ack

	// parse and validate ReadFromBeginning
	readFromBeginning, err := parseBool(cfg, ReadFromBeginning, false)
	if err != nil {
		return Config{}, cerrors.Errorf("invalid value for ReadFromBeginning: %w", err)
	}
	parsed.ReadFromBeginning = readFromBeginning

	// parse and validate delivery DeliveryTimeout
	timeout, err := parseDuration(cfg, DeliveryTimeout, 10*time.Second)
	if err != nil {
		return Config{}, cerrors.Errorf("invalid delivery timeout: %w", err)
	}
	// it makes no sense to expect a message to be delivered immediately
	if timeout == 0 {
		return Config{}, cerrors.New("invalid delivery timeout: has to be > 0ms")
	}
	parsed.DeliveryTimeout = timeout
	return parsed, nil
}

func parseAcks(ack string) (skafka.RequiredAcks, error) {
	// when ack is empty, return default (which is 'all')
	if ack == "" {
		return skafka.RequireAll, nil
	}
	acks := skafka.RequiredAcks(0)
	err := acks.UnmarshalText([]byte(ack))
	if err != nil {
		return 0, cerrors.Errorf("unknown ack mode: %w", err)
	}
	return acks, nil
}

func parseBool(cfg map[string]string, key string, defaultVal bool) (bool, error) {
	boolString, exists := cfg[key]
	if !exists {
		return defaultVal, nil
	}
	parsed, err := strconv.ParseBool(boolString)
	if err != nil {
		return false, cerrors.Errorf("value for key %s cannot be parsed: %w", key, err)
	}
	return parsed, nil
}

func parseDuration(cfg map[string]string, key string, defaultVal time.Duration) (time.Duration, error) {
	timeoutStr, exists := cfg[key]
	if !exists {
		return defaultVal, nil
	}
	timeout, err := time.ParseDuration(timeoutStr)
	if err != nil {
		return 0, cerrors.Errorf("duration cannot be parsed: %w", err)
	}
	return timeout, nil
}

func checkRequired(cfg map[string]string) error {
	for _, reqKey := range Required {
		_, exists := cfg[reqKey]
		if !exists {
			return requiredConfigErr(reqKey)
		}
	}
	return nil
}

func requiredConfigErr(name string) error {
	return cerrors.Errorf("%q config value must be set", name)
}

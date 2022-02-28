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
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"io/ioutil"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/google/uuid"
	"github.com/segmentio/kafka-go"
)

// Consumer represents a Kafka consumer in a simplified form,
// with just the functionality which is needed for this plugin.
// A Consumer's offset is being managed by the broker.
type Consumer interface {
	// StartFrom instructs the consumer to connect to a broker and a topic, using the provided consumer group ID.
	// The group ID is significant for this consumer's offsets.
	// By using the same group ID after a restart, we make sure that the consumer continues from where it left off.
	// Returns: An error, if the consumer could not be set to read from the given position, nil otherwise.
	StartFrom(config Config, groupID string) error

	// Get returns a message from the configured topic. Waits until a messages is available
	// or until it errors out.
	// Returns: a message (if available), the consumer group ID and an error (if there was one).
	Get(ctx context.Context) (*kafka.Message, string, error)

	Ack() error

	// Close this consumer and the associated resources (e.g. connections to the broker)
	Close()
}

type segmentConsumer struct {
	reader      *kafka.Reader
	lastMsgRead *kafka.Message
}

// NewConsumer creates a new Kafka consumer. The consumer needs to be started
// (using the StartFrom method) before actually being used.
func NewConsumer() (Consumer, error) {
	return &segmentConsumer{}, nil
}

func (c *segmentConsumer) StartFrom(config Config, groupID string) error {
	// todo if we can assume that a new Config instance will always be created by calling Parse(),
	// and that the instance will not be mutated, then we can leave it out these checks.
	if len(config.Servers) == 0 {
		return ErrServersMissing
	}
	if config.Topic == "" {
		return ErrTopicMissing
	}
	reader, err := newReader(config, groupID)
	if err != nil {
		return cerrors.Errorf("couldn't create reader: %w")
	}
	c.reader = reader
	return nil
}

func newReader(cfg Config, groupID string) (*kafka.Reader, error) {
	readerCfg := kafka.ReaderConfig{
		Brokers:               cfg.Servers,
		Topic:                 cfg.Topic,
		WatchPartitionChanges: true,
	}
	// Group ID
	if groupID == "" {
		readerCfg.GroupID = uuid.NewString()
	} else {
		readerCfg.GroupID = groupID
	}
	// StartOffset
	if cfg.ReadFromBeginning {
		readerCfg.StartOffset = kafka.FirstOffset
	} else {
		readerCfg.StartOffset = kafka.LastOffset
	}
	// TLS config
	if cfg.ClientCertFile != "" {
		dialer, err := newTLSDialer(cfg)
		if err != nil {
			return nil, cerrors.Errorf("couldn't create dialer: %w", err)
		}
		readerCfg.Dialer = dialer
	}
	return kafka.NewReader(readerCfg), nil
}

func newTLSDialer(cfg Config) (*kafka.Dialer, error) {
	tlsCfg, err := newTLSConfig(cfg.ClientCertFile, cfg.ClientKeyFile, cfg.CACertFile, cfg.InsecureSkipVerify)
	if err != nil {
		return nil, cerrors.Errorf("invalid TLS config: %w", err)
	}
	return &kafka.Dialer{
		ClientID:  "",
		DualStack: true,
		TLS:       tlsCfg,
	}, nil
}

func newTLSConfig(clientCertFile, clientKeyFile, caCertFile string, serverNoVerify bool) (*tls.Config, error) {
	tlsConfig := tls.Config{}

	// Load client cert
	cert, err := tls.LoadX509KeyPair(clientCertFile, clientKeyFile)
	if err != nil {
		return nil, err
	}
	tlsConfig.Certificates = []tls.Certificate{cert}

	// Load CA cert
	caCert, err := ioutil.ReadFile(caCertFile)
	if err != nil {
		return nil, err
	}
	caCertPool := x509.NewCertPool()
	caCertPool.AppendCertsFromPEM(caCert)
	tlsConfig.RootCAs = caCertPool

	tlsConfig.InsecureSkipVerify = true
	return &tlsConfig, err
}

func (c *segmentConsumer) Get(ctx context.Context) (*kafka.Message, string, error) {
	msg, err := c.reader.FetchMessage(ctx)
	if err != nil {
		return nil, "", cerrors.Errorf("couldn't read message: %w", err)
	}
	c.lastMsgRead = &msg
	return &msg, c.readerID(), nil
}

func (c *segmentConsumer) Ack() error {
	err := c.reader.CommitMessages(context.Background(), *c.lastMsgRead)
	if err != nil {
		return cerrors.Errorf("couldn't commit messages: %w", err)
	}
	return nil
}

func (c *segmentConsumer) Close() {
	if c.reader == nil {
		return
	}
	// this will also make the loops in the reader goroutines stop
	err := c.reader.Close()
	if err != nil {
		fmt.Printf("couldn't close reader: %v\n", err)
	}
}

func (c *segmentConsumer) readerID() string {
	return c.reader.Config().GroupID
}

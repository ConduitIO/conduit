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

package kafka_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/plugins/kafka"
	"github.com/conduitio/conduit/pkg/plugins/kafka/mock"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
	skafka "github.com/segmentio/kafka-go"
)

func TestConfigureSource_FailsWhenConfigEmpty(t *testing.T) {
	underTest := kafka.Source{}
	err := underTest.Configure(context.Background(), make(map[string]string))
	assert.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "config is invalid:"), "incorrect error msg")
}

func TestConfigureSource_FailsWhenConfigInvalid(t *testing.T) {
	underTest := kafka.Source{}
	err := underTest.Configure(context.Background(), map[string]string{"foobar": "foobar"})
	assert.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "config is invalid:"), "incorrect error msg")
}

func TestTeardownSource_ClosesClient(t *testing.T) {
	ctrl := gomock.NewController(t)

	consumerMock := mock.NewConsumer(ctrl)
	consumerMock.
		EXPECT().
		Close().
		Return()

	underTest := kafka.Source{Consumer: consumerMock, Config: connectorCfg()}
	assert.Ok(t, underTest.Teardown(context.Background()))
}

func TestReadPosition(t *testing.T) {
	ctrl := gomock.NewController(t)

	kafkaMsg := testKafkaMsg()
	cfg := connectorCfg()
	groupID := uuid.NewString()

	consumerMock := mock.NewConsumer(ctrl)
	consumerMock.
		EXPECT().
		StartFrom(cfg, groupID)
	consumerMock.
		EXPECT().
		Get(gomock.Any()).
		Return(kafkaMsg, groupID, nil)

	underTest := kafka.Source{Consumer: consumerMock, Config: cfg}
	rec, err := underTest.Read(context.Background())
	assert.Ok(t, err)
	assert.Equal(t, rec.Key.Bytes(), kafkaMsg.Key)
	assert.Equal(t, rec.Payload.Bytes(), kafkaMsg.Value)

	assert.Equal(t, groupID, string(rec.Position))
}

func TestRead_StartFromCalledOnce(t *testing.T) {
	ctrl := gomock.NewController(t)

	cfg := connectorCfg()
	pos := uuid.NewString()

	consumerMock := mock.NewConsumer(ctrl)
	consumerMock.
		EXPECT().
		StartFrom(cfg, pos)
	consumerMock.
		EXPECT().
		Get(gomock.Any()).
		Return(testKafkaMsg(), pos, nil).
		Times(2)

	underTest := kafka.Source{Consumer: consumerMock, Config: cfg}
	_, err := underTest.Read(context.Background())
	assert.Ok(t, err)
	_, err = underTest.Read(context.Background())
	assert.Ok(t, err)
}

func TestRead(t *testing.T) {
	ctrl := gomock.NewController(t)

	kafkaMsg := testKafkaMsg()
	cfg := connectorCfg()
	pos := uuid.NewString()

	consumerMock := mock.NewConsumer(ctrl)
	consumerMock.
		EXPECT().
		StartFrom(cfg, pos)
	consumerMock.
		EXPECT().
		Get(gomock.Any()).
		Return(kafkaMsg, pos, nil)

	underTest := kafka.Source{Consumer: consumerMock, Config: cfg}
	rec, err := underTest.Read(context.Background())
	assert.Ok(t, err)
	assert.Equal(t, rec.Key.Bytes(), kafkaMsg.Key)
	assert.Equal(t, rec.Payload.Bytes(), kafkaMsg.Value)
	assert.Equal(t, pos, string(rec.Position))
}

func testKafkaMsg() *skafka.Message {
	return &skafka.Message{
		Topic:         "test",
		Partition:     0,
		Offset:        123,
		HighWaterMark: 234,
		Key:           []byte("test-key"),
		Value:         []byte("test-value"),
		Time:          time.Time{},
	}
}

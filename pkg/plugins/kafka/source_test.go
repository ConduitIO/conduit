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
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/plugins/kafka/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/confluentinc/confluent-kafka-go/kafka"
	"github.com/golang/mock/gomock"
)

func TestOpenSource_FailsWhenConfigEmpty(t *testing.T) {
	underTest := Source{}
	err := underTest.Open(context.TODO(), plugins.Config{})
	assert.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "config is invalid:"), "incorrect error msg")
}

func TestOpenSource_FailsWhenConfigInvalid(t *testing.T) {
	underTest := Source{}
	err := underTest.Open(context.TODO(), plugins.Config{Settings: map[string]string{"foobar": "foobar"}})
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

	underTest := Source{Consumer: consumerMock, Config: connectorCfg()}
	assert.Ok(t, underTest.Teardown())
}

func TestRead_SpecialPositions(t *testing.T) {
	testCases := []struct {
		name string
		pos  record.Position
	}{
		{
			name: "empty position",
			pos:  record.Position{},
		},
		{
			name: "nil position",
			pos:  nil,
		},
	}

	for _, tt := range testCases {
		tt := tt
		t.Run(tt.name, func(t *testing.T) {
			testReadPosition(t, tt.pos)
		})
	}
}

func testReadPosition(t *testing.T, pos record.Position) {
	ctrl := gomock.NewController(t)

	kafkaMsg := testKafkaMsg()
	cfg := connectorCfg()
	expPos := map[int32]int64{}

	consumerMock := mock.NewConsumer(ctrl)
	consumerMock.
		EXPECT().
		StartFrom(cfg.Topic, map[int32]int64{}, cfg.ReadFromBeginning)
	consumerMock.
		EXPECT().
		Get(msgTimeout).
		Return(kafkaMsg, expPos, nil)

	underTest := Source{Consumer: consumerMock, Config: cfg}
	rec, err := underTest.Read(context.TODO(), pos)
	assert.Ok(t, err)
	assert.Equal(t, rec.Key.Bytes(), kafkaMsg.Key)
	assert.Equal(t, rec.Payload.Bytes(), kafkaMsg.Value)

	var actPos map[int32]int64
	err = json.Unmarshal(rec.Position, &actPos)
	assert.Ok(t, err)
	assert.Equal(t, expPos, actPos)
}

func TestRead_StartFromCalledOnce(t *testing.T) {
	ctrl := gomock.NewController(t)

	cfg := connectorCfg()
	pos1 := map[int32]int64{0: 122, 1: 455}
	pos1Bytes, _ := json.Marshal(pos1)

	pos2 := map[int32]int64{0: 122, 1: 456}
	pos2Bytes, _ := json.Marshal(pos2)

	consumerMock := mock.NewConsumer(ctrl)
	consumerMock.
		EXPECT().
		StartFrom(cfg.Topic, pos1, cfg.ReadFromBeginning)
	consumerMock.
		EXPECT().
		Get(msgTimeout).
		Return(testKafkaMsg(), pos2, nil).
		Times(1)
	consumerMock.
		EXPECT().
		Get(msgTimeout).
		Return(nil, pos2, nil).
		Times(1)

	underTest := Source{Consumer: consumerMock, Config: cfg}
	_, err := underTest.Read(context.TODO(), pos1Bytes)
	assert.Ok(t, err)
	_, err = underTest.Read(context.TODO(), pos2Bytes)
	assert.True(t, plugins.IsRecoverableError(err), "expected recoverable error")
}

func TestRead(t *testing.T) {
	ctrl := gomock.NewController(t)

	kafkaMsg := testKafkaMsg()
	cfg := connectorCfg()
	startPos := map[int32]int64{0: 122, 1: 455}
	startPosBytes, _ := json.Marshal(startPos)
	expPos := map[int32]int64{0: 123, 1: 456}

	consumerMock := mock.NewConsumer(ctrl)
	consumerMock.
		EXPECT().
		StartFrom(cfg.Topic, startPos, cfg.ReadFromBeginning)
	consumerMock.
		EXPECT().
		Get(msgTimeout).
		Return(kafkaMsg, expPos, nil)

	underTest := Source{Consumer: consumerMock, Config: cfg}
	rec, err := underTest.Read(context.TODO(), startPosBytes)
	assert.Ok(t, err)
	assert.Equal(t, rec.Key.Bytes(), kafkaMsg.Key)
	assert.Equal(t, rec.Payload.Bytes(), kafkaMsg.Value)

	var actPos map[int32]int64
	err = json.Unmarshal(rec.Position, &actPos)
	assert.Ok(t, err)
	assert.Equal(t, expPos, actPos)
}

func TestRead_InvalidPosition(t *testing.T) {
	underTest := Source{}
	rec, err := underTest.Read(context.TODO(), []byte("foobar"))
	assert.Equal(t, record.Record{}, rec)
	assert.Error(t, err)
	assert.True(
		t,
		strings.HasPrefix(err.Error(), "couldn't start from position: invalid position"),
		"expected msg to have prefix 'couldn't start from position: invalid position'",
	)
}

func TestRead_NilMsgReturned(t *testing.T) {
	ctrl := gomock.NewController(t)
	cfg := connectorCfg()

	consumerMock := mock.NewConsumer(ctrl)
	consumerMock.
		EXPECT().
		StartFrom(cfg.Topic, map[int32]int64{}, cfg.ReadFromBeginning)
	consumerMock.
		EXPECT().
		Get(msgTimeout).
		Return(nil, map[int32]int64{}, nil)

	underTest := Source{Consumer: consumerMock, Config: cfg}
	rec, err := underTest.Read(context.TODO(), record.Position{})
	assert.Equal(t, record.Record{}, rec)
	assert.Error(t, err)
	assert.True(t, plugins.IsRecoverableError(err), "expected a recoverable error")
}

func testKafkaMsg() *kafka.Message {
	return &kafka.Message{
		TopicPartition: kafka.TopicPartition{},
		Value:          []byte("test-value"),
		Key:            []byte("test-key"),
		Timestamp:      time.Time{},
		TimestampType:  0,
		Opaque:         nil,
		Headers:        nil,
	}
}

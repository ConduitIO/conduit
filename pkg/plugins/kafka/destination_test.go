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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
	"github.com/conduitio/conduit/pkg/plugins/kafka"
	"github.com/conduitio/conduit/pkg/plugins/kafka/mock"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
)

func TestConfigureDestination_FailsWhenConfigEmpty(t *testing.T) {
	underTest := kafka.Destination{}
	err := underTest.Configure(context.Background(), make(map[string]string))
	assert.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "config is invalid:"), "incorrect error msg")
}

func TestConfigureDestination_FailsWhenConfigInvalid(t *testing.T) {
	underTest := kafka.Destination{}
	err := underTest.Configure(context.Background(), map[string]string{"foobar": "foobar"})
	assert.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "config is invalid:"), "incorrect error msg")
}

func TestConfigureDestination_KafkaProducerCreated(t *testing.T) {
	underTest := kafka.Destination{}
	err := underTest.Configure(context.Background(), configMap())
	assert.Ok(t, err)

	err = underTest.Open(context.Background())
	assert.Ok(t, err)
	assert.NotNil(t, underTest.Client)
	defer underTest.Client.Close()
}

func TestTeardown_ClosesClient(t *testing.T) {
	ctrl := gomock.NewController(t)

	clientMock := mock.NewProducer(ctrl)
	clientMock.
		EXPECT().
		Close().
		Return()

	underTest := kafka.Destination{Client: clientMock, Config: connectorCfg()}
	assert.Ok(t, underTest.Teardown(context.Background()))
}
func TestTeardown_NoOpen(t *testing.T) {
	underTest := kafka.NewDestination()
	assert.Ok(t, underTest.Teardown(context.Background()))
}

func TestWrite_ClientSendsMessage(t *testing.T) {
	ctrl := gomock.NewController(t)

	rec := testRec()

	clientMock := mock.NewProducer(ctrl)
	clientMock.
		EXPECT().
		Send(
			gomock.Eq(rec.Key.Bytes()),
			gomock.Eq(rec.Payload.Bytes()),
		).
		Return(nil)

	underTest := kafka.Destination{Client: clientMock, Config: connectorCfg()}

	err := underTest.Write(context.Background(), rec)
	assert.Ok(t, err)
}

func connectorCfg() kafka.Config {
	cfg, _ := kafka.Parse(configMap())
	return cfg
}

func configMap() map[string]string {
	return map[string]string{kafka.Servers: "localhost:9092", kafka.Topic: "test"}
}

func testRec() sdk.Record {
	return sdk.Record{
		Position:  []byte(uuid.NewString()),
		Metadata:  nil,
		CreatedAt: time.Time{},
		Key:       sdk.RawData(uuid.NewString()),
		Payload:   sdk.RawData(fmt.Sprintf("test message %s", time.Now())),
	}
}

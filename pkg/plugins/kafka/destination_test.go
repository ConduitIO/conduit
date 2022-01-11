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
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/assert"
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/plugins/kafka/mock"
	"github.com/conduitio/conduit/pkg/record"
	"github.com/golang/mock/gomock"
	"github.com/google/uuid"
)

func TestOpen_FailsWhenConfigEmpty(t *testing.T) {
	underTest := Destination{}
	err := underTest.Open(context.TODO(), plugins.Config{})
	assert.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "config is invalid:"), "incorrect error msg")
}

func TestOpen_FailsWhenConfigInvalid(t *testing.T) {
	underTest := Destination{}
	err := underTest.Open(context.TODO(), plugins.Config{Settings: map[string]string{"foobar": "foobar"}})
	assert.Error(t, err)
	assert.True(t, strings.HasPrefix(err.Error(), "config is invalid:"), "incorrect error msg")
}

func TestOpen_KafkaProducerCreated(t *testing.T) {
	underTest := Destination{}
	err := underTest.Open(context.TODO(), config())
	assert.Ok(t, err)
	assert.NotNil(t, underTest.Client)
}

func TestTeardown_ClosesClient(t *testing.T) {
	ctrl := gomock.NewController(t)

	clientMock := mock.NewProducer(ctrl)
	clientMock.
		EXPECT().
		Close().
		Return()

	underTest := Destination{Client: clientMock, Config: connectorCfg()}
	assert.Ok(t, underTest.Teardown())
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

	underTest := Destination{Client: clientMock, Config: connectorCfg()}

	res, err := underTest.Write(context.TODO(), rec)
	assert.Ok(t, err)
	assert.NotNil(t, res)
}

func connectorCfg() Config {
	cfg, _ := Parse(configMap())
	return cfg
}

func config() plugins.Config {
	return plugins.Config{Settings: configMap()}
}

func configMap() map[string]string {
	return map[string]string{Servers: "localhost:9092", Topic: "test"}
}

func testRec() record.Record {
	return record.Record{
		Position:  []byte(uuid.NewString()),
		Metadata:  nil,
		CreatedAt: time.Time{},
		ReadAt:    time.Time{},
		Key:       record.RawData{Raw: []byte(uuid.NewString())},
		Payload:   record.RawData{Raw: []byte(fmt.Sprintf("test message %s", time.Now()))},
	}
}

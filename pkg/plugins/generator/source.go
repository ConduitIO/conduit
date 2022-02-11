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

package generator

import (
	"context"
	"encoding/json"
	"fmt"
	"math/rand"
	"strconv"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/plugin/sdk"
)

// Source connector
type Source struct {
	sdk.UnimplementedSource

	created int64
	Config  Config
}

func NewSource() sdk.Source {
	return &Source{}
}

func (s *Source) Configure(ctx context.Context, config map[string]string) error {
	parsedCfg, err := Parse(config)
	if err != nil {
		return cerrors.Errorf("invalid config: %w", err)
	}
	s.Config = parsedCfg
	return nil
}

func (s *Source) Open(ctx context.Context, position sdk.Position) error {
	return nil // nothing to start
}

func (s *Source) Read(ctx context.Context) (sdk.Record, error) {
	if s.created >= s.Config.RecordCount && s.Config.RecordCount >= 0 {
		// nothing more to produce, block until context is done
		<-ctx.Done()
		return sdk.Record{}, ctx.Err()
	}
	s.created++

	if s.Config.ReadTime > 0 {
		time.Sleep(s.Config.ReadTime)
	}
	data, err := s.toRawData(s.newRecord(s.created))
	if err != nil {
		return sdk.Record{}, err
	}
	return sdk.Record{
		Position:  []byte(strconv.FormatInt(s.created, 10)),
		Metadata:  nil,
		Key:       sdk.RawData(fmt.Sprintf("key #%d", s.created)),
		Payload:   data,
		CreatedAt: time.Now(),
	}, nil
}

func (s *Source) Ack(ctx context.Context, position sdk.Position) error {
	sdk.Logger(ctx).Debug().Str("position", string(position)).Msg("got ack")
	return nil // no ack needed
}

func (s *Source) Teardown(ctx context.Context) error {
	return nil // nothing to stop
}

func (s *Source) newRecord(i int64) map[string]interface{} {
	rec := make(map[string]interface{})
	for name, typeString := range s.Config.Fields {
		rec[name] = s.newDummyValue(typeString, i)
	}
	return rec
}

func (s *Source) newDummyValue(typeString string, i int64) interface{} {
	switch typeString {
	case "int":
		return rand.Int31() //nolint:gosec // security not important here
	case "string":
		return fmt.Sprintf("string %v", i)
	case "time":
		return time.Now()
	case "bool":
		return rand.Int()%2 == 0 //nolint:gosec // security not important here
	default:
		panic(cerrors.New("invalid field"))
	}
}

func (s *Source) toRawData(rec map[string]interface{}) (sdk.Data, error) {
	bytes, err := json.Marshal(rec)
	if err != nil {
		return nil, cerrors.Errorf("couldn't serialize data: %w", err)
	}
	return sdk.RawData(bytes), nil
}

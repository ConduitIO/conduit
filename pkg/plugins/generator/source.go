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
	"github.com/conduitio/conduit/pkg/plugins"
	"github.com/conduitio/conduit/pkg/record"
)

// Source connector
type Source struct {
	created int64
	Config  Config
}

func (s *Source) Ack(ctx context.Context, position record.Position) error {
	return nil // no ack needed
}

func (s *Source) Open(ctx context.Context, config plugins.Config) error {
	parsedCfg, err := Parse(config)
	if err != nil {
		return cerrors.Errorf("invalid config: %w", err)
	}
	s.Config = parsedCfg
	return nil
}

func (s *Source) Read(ctx context.Context, p record.Position) (record.Record, error) {
	if s.created >= s.Config.RecordCount && s.Config.RecordCount >= 0 {
		return record.Record{}, plugins.ErrEndData
	}
	s.created++

	if s.Config.ReadTime > 0 {
		time.Sleep(s.Config.ReadTime)
	}
	data, err := s.toRawData(s.newRecord(s.created))
	if err != nil {
		return record.Record{}, err
	}
	return record.Record{
		Position:  []byte(strconv.FormatInt(s.created, 10)),
		Metadata:  nil,
		Key:       record.RawData{Raw: []byte(fmt.Sprintf("key #%d", s.created))},
		Payload:   data,
		CreatedAt: time.Now(),
	}, nil
}

func (s *Source) newRecord(i int64) map[string]interface{} {
	rec := make(map[string]interface{})
	for name, typeString := range s.Config.Fields {
		rec[name] = s.newDummyValue(typeString, i)
	}
	return rec
}

func (s *Source) Teardown() error {
	return nil
}

func (s *Source) Validate(cfg plugins.Config) error {
	_, err := Parse(cfg)
	if err != nil {
		return cerrors.Errorf("invalid config: %w", err)
	}
	return nil
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

func (s *Source) toRawData(rec map[string]interface{}) (record.Data, error) {
	bytes, err := json.Marshal(rec)
	if err != nil {
		return nil, cerrors.Errorf("couldn't serialize data: %w", err)
	}
	return record.RawData{
		Raw: bytes,
	}, nil
}

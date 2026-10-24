// Copyright © 2026 Meroxa, Inc.
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

package internal

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/schemaregistry"
	"github.com/conduitio/conduit/pkg/schemaregistry/schemaregistrytest"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/sr"
)

const testSchema = `{"type":"record","name":"r","fields":[{"name":"a","type":"string"}]}`

func registryWithSchema(t *testing.T, schema string) (schemaregistry.Registry, string, int) {
	t.Helper()
	is := is.New(t)
	client, err := schemaregistry.NewClient(log.Nop(), sr.URLs(schemaregistrytest.TestSchemaRegistryURL(t)))
	is.NoErr(err)
	subject := fmt.Sprintf("internal-%s-%d", strings.ReplaceAll(t.Name(), "/", "-"), time.Now().UnixNano())
	ss, err := client.CreateSchema(context.Background(), subject, sr.Schema{Type: sr.TypeAvro, Schema: schema})
	is.NoErr(err)
	return client, subject, ss.ID
}

func zigzagLong(n int64) []byte {
	return binary.AppendUvarint(nil, uint64((n<<1)^(n>>63)))
}

func TestDecoder_Decode(t *testing.T) {
	registry, subject, id := registryWithSchema(t, testSchema)
	ctx := context.Background()
	d := NewDecoder(registry, log.Nop())
	header, err := (&sr.ConfluentHeader{}).AppendEncode(nil, id, nil)
	if err != nil {
		t.Fatal(err)
	}

	t.Run("a valid record", func(t *testing.T) {
		is := is.New(t)
		e := NewEncoder(registry, log.Nop(), DownloadSchemaStrategy{Subject: subject, Version: 1})
		raw, err := e.Encode(ctx, opencdc.StructuredData{"a": "hello"})
		is.NoErr(err)
		got, err := d.Decode(ctx, raw)
		is.NoErr(err)
		is.Equal(got, opencdc.StructuredData{"a": "hello"})
	})
	t.Run("no header", func(t *testing.T) {
		is := is.New(t)
		_, err := d.Decode(ctx, opencdc.RawData("x"))
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "failed to decode header"))
	})
	t.Run("unknown schema", func(t *testing.T) {
		is := is.New(t)
		h, err := (&sr.ConfluentHeader{}).AppendEncode(nil, id+100000, nil)
		is.NoErr(err)
		_, err = d.Decode(ctx, opencdc.RawData(h))
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "failed to get schema"))
	})
	t.Run("codec failure does not repeat the value", func(t *testing.T) {
		is := is.New(t)
		// field a claims 60 bytes and carries a short secret
		raw := append(append(append([]byte{}, header...), zigzagLong(60)...), []byte("customer-SECRET-4711")...)
		_, err := d.Decode(ctx, opencdc.RawData(raw))
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "failed to unmarshal data with schema"))
		is.True(strings.Contains(err.Error(), "details not shown"))
		is.True(!strings.Contains(err.Error(), "SECRET"))
	})
}

func TestEncoder_Encode(t *testing.T) {
	registry, subject, _ := registryWithSchema(t, testSchema)
	ctx := context.Background()

	t.Run("unknown subject", func(t *testing.T) {
		is := is.New(t)
		e := NewEncoder(registry, log.Nop(), DownloadSchemaStrategy{Subject: "no-such-subject", Version: 1})
		_, err := e.Encode(ctx, opencdc.StructuredData{"a": "x"})
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "failed to get schema"))
	})
	t.Run("codec failure does not repeat the value", func(t *testing.T) {
		is := is.New(t)
		e := NewEncoder(registry, log.Nop(), DownloadSchemaStrategy{Subject: subject, Version: 1})
		_, err := e.Encode(ctx, opencdc.StructuredData{"a": map[string]any{"customer-SECRET-4711": 1}})
		is.True(err != nil)
		is.True(strings.Contains(err.Error(), "failed to marshal data with schema"))
		is.True(strings.Contains(err.Error(), "details not shown"))
		is.True(!strings.Contains(err.Error(), "SECRET"))
	})
}

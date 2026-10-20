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

package debezium

import (
	"testing"
	"time"

	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/matryer/is"
)

func TestProcessorSpecification(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	p := NewProcessor(log.Nop())
	spec, err := p.Specification()
	is.NoErr(err)
	is.Equal(spec.Name, "debezium.encode")
	is.True(spec.Parameters != nil)
}

func TestProcessorTeardown(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	p := NewProcessor(log.Nop())
	is.NoErr(p.Teardown(t.Context()))
}

func TestProcessorProcessSuccess(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	p := NewProcessor(log.Nop())
	p.now = func() time.Time { return time.UnixMilli(1792045811910) }
	err := p.Configure(t.Context(), map[string]string{
		"key.converter":             "json",
		"value.converter":           "json",
		"key.json.schemas.enable":   "true",
		"value.json.schemas.enable": "true",
		"json.decimal.format":       "BASE64",
		"tombstones.on.delete":      "true",
		"debezium.compat":           "3.5",
	})
	is.NoErr(err)

	rec := opencdc.Record{
		Key:       opencdc.StructuredData{"seq": int32(1), "region": "us"},
		Payload:   opencdc.Change{After: opencdc.StructuredData{"region": "us", "seq": int32(1), "note": "second"}},
		Metadata:  compositeMetadata("c", ""),
		Operation: opencdc.OperationCreate,
	}
	out := p.Process(t.Context(), []opencdc.Record{rec})
	is.Equal(len(out), 1)

	single, ok := out[0].(sdk.SingleRecord)
	is.True(ok)
	is.Equal(single.Metadata[metaPlanVersion], planVersion)
	is.Equal(single.Payload.Before, nil)

	raw := single.Payload.After.(opencdc.RawData)
	var f frame
	err = json.Unmarshal(raw.Bytes(), &f)
	is.NoErr(err)
	is.Equal(f.Version, 1)
	is.Equal(len(f.Messages), 1)
	is.Equal(f.Messages[0].Topic, "dbz.golden.composite")
}

func TestProcessorProcessError(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	p := NewProcessor(log.Nop())
	err := p.Configure(t.Context(), map[string]string{"key.converter": "json", "value.converter": "json"})
	is.NoErr(err)

	rec := opencdc.Record{Metadata: opencdc.Metadata{}}
	out := p.Process(t.Context(), []opencdc.Record{rec})
	is.Equal(len(out), 1)

	errRec, ok := out[0].(sdk.ErrorRecord)
	is.True(ok)
	is.True(errRec.Error != nil)
}

func TestUpdateWithoutOldKey(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	rec := opencdc.Record{
		Key: opencdc.StructuredData{"seq": int32(1), "region": "us"},
		Payload: opencdc.Change{
			Before: opencdc.StructuredData{"region": "us", "seq": int32(1), "note": "old"},
			After:  opencdc.StructuredData{"region": "us", "seq": int32(1), "note": "new"},
		},
		Metadata:  compositeMetadata("u", ""),
		Operation: opencdc.OperationUpdate,
	}
	msgs := encodeRecord(t, rec, newEncodeConfig(converterJSON, decimalBase64, true, true))
	is.Equal(len(msgs), 1)

	var val map[string]any
	err := json.Unmarshal([]byte(decodeStr(t, msgs[0].Value)), &val)
	is.NoErr(err)
	is.Equal(val["payload"].(map[string]any)["op"], "u")
}

func expandPlanErr(t *testing.T, rec opencdc.Record, cfg encodeConfig) error {
	t.Helper()
	d, err := parseDescriptor(rec.Metadata[metaDescriptor])
	if err != nil {
		t.Fatalf("parseDescriptor: %v", err)
	}
	_, err = expandPlan(rec, d, cfg, time.UnixMilli(1792045811910))
	return err
}

func TestExpandPlanErrors(t *testing.T) {
	t.Parallel()
	is := is.New(t)
	cfg := newEncodeConfig(converterJSON, decimalBase64, true, true)

	t.Run("missing_topic", func(t *testing.T) {
		md := compositeMetadata("c", "")
		delete(md, metaTopic)
		err := expandPlanErr(t, opencdc.Record{Metadata: md}, cfg)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, CodeDescriptorMissing)
	})

	t.Run("missing_source_db", func(t *testing.T) {
		md := compositeMetadata("c", "")
		delete(md, metaSourcePfx+srcDB)
		err := expandPlanErr(t, opencdc.Record{Metadata: md}, cfg)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, CodeDescriptorMissing)
	})

	t.Run("missing_ts_ms", func(t *testing.T) {
		md := compositeMetadata("c", "")
		delete(md, metaSourcePfx+srcTSMs)
		err := expandPlanErr(t, opencdc.Record{Metadata: md}, cfg)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, CodeDescriptorMissing)
	})

	t.Run("unsupported_op", func(t *testing.T) {
		md := compositeMetadata("x", "")
		err := expandPlanErr(t, opencdc.Record{Metadata: md}, cfg)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, CodeUnsupportedType)
	})

	t.Run("bad_old_key", func(t *testing.T) {
		md := compositeMetadata("u", "not-json")
		err := expandPlanErr(t, opencdc.Record{Metadata: md}, cfg)
		ce, ok := conduiterr.Get(err)
		is.True(ok)
		is.Equal(ce.Code, CodeOldKeyUnavailable)
	})
}

func TestEncodeFrame(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	b, err := encodeFrame([]message{{Topic: "t"}})
	is.NoErr(err)
	is.Equal(string(b), `{"version":1,"messages":[{"topic":"t","key":null,"value":null}]}`)
}

func TestRecordKey(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	m, err := recordKey(opencdc.StructuredData{"a": "b"})
	is.NoErr(err)
	is.Equal(m["a"], "b")

	m, err = recordKey(nil)
	is.NoErr(err)
	is.Equal(m, nil)

	_, err = recordKey(opencdc.RawData("x"))
	is.True(err != nil)
}

func TestTransactionPayload(t *testing.T) {
	t.Parallel()
	is := is.New(t)

	is.Equal(transactionPayload(opencdc.Metadata{}), nil)

	md := opencdc.Metadata{metaTxID: "777", metaTxTotal: "10", metaTxCollOrder: "5"}
	tx := transactionPayload(md)
	is.Equal(tx["id"], "777")
	is.Equal(tx["total_order"], int64(10))
	is.Equal(tx["data_collection_order"], int64(5))
}

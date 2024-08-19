// Copyright © 2024 Meroxa, Inc.
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

package unwrap

import (
	"context"
	"testing"

	"github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit-commons/opencdc"
	sdk "github.com/conduitio/conduit-processor-sdk"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/plugin/processor/builtin/internal"
	"github.com/google/go-cmp/cmp"
	"github.com/matryer/is"
)

const RecordUpdateWithBefore = `{
        "position": "NzgyNjJmODUtODNmMS00ZGQwLWEyZDAtNTRmNjA1ZjkyYTg0",
        "operation": "update",
        "metadata": {
          "conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
          "opencdc.readAt": "1706028953595546000",
          "opencdc.version": "v1"
        },
        "key": "MTc3NzQ5NDEtNTdhMi00MmZhLWI0MzAtODkxMmE5NDI0YjNh",
        "payload": {
          "before": {
            "event_id": 1747353650,
            "msg": "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
            "pg_generator": false,
            "sensor_id": 1250383582,
            "triggered": false
          },
          "after": {
            "event_id": 1747353658,
            "msg": "string 0f5397c9-31f1-422a-9c9a-26e3574a5c31",
            "pg_generator": false,
            "sensor_id": 1250383580,
            "triggered": false
          }
        }
      }`

const RecordUpdateNoBefore = `{
        "position": "NzgyNjJmODUtODNmMS00ZGQwLWEyZDAtNTRmNjA1ZjkyYTg0",
        "operation": "update",
        "metadata": {
          "conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
          "opencdc.readAt": "1706028953595546000",
          "opencdc.version": "v1"
        },
        "key": "MTc3NzQ5NDEtNTdhMi00MmZhLWI0MzAtODkxMmE5NDI0YjNh",
        "payload": {
          "before": null,
          "after": {
            "event_id": 1747353650,
            "msg": "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
            "pg_generator": false,
            "sensor_id": 1250383582,
            "triggered": false
          }
        }
      }`

const RecordDeleteNoBefore = `{
        "position": "NzgyNjJmODUtODNmMS00ZGQwLWEyZDAtNTRmNjA1ZjkyYTg0",
        "operation": "delete",
        "metadata": {
          "conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
          "opencdc.readAt": "1706028953595546000",
          "opencdc.version": "v1"
        },
        "key": "MTc3NzQ5NDEtNTdhMi00MmZhLWI0MzAtODkxMmE5NDI0YjNh",
        "payload": {
          "before": null,
          "after": null
        }
      }`

const RecordDeleteWithBefore = `{
        "position": "NzgyNjJmODUtODNmMS00ZGQwLWEyZDAtNTRmNjA1ZjkyYTg0",
        "operation": "delete",
        "metadata": {
          "conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
          "opencdc.readAt": "1706028953595546000",
          "opencdc.version": "v1"
        },
        "key": "MTc3NzQ5NDEtNTdhMi00MmZhLWI0MzAtODkxMmE5NDI0YjNh",
        "payload": {
          "before": {
            "event_id": 1747353650,
            "msg": "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
            "pg_generator": false,
            "sensor_id": 1250383582,
            "triggered": false
          },
          "after": null
        }
      }`

const RecordCreate = `{
        "position": "NzgyNjJmODUtODNmMS00ZGQwLWEyZDAtNTRmNjA1ZjkyYTg0",
        "operation": "create",
        "metadata": {
          "conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
          "opencdc.readAt": "1706028953595546000",
          "opencdc.version": "v1"
        },
        "key": "MTc3NzQ5NDEtNTdhMi00MmZhLWI0MzAtODkxMmE5NDI0YjNh",
        "payload": {
          "before": null,
          "after": {
            "event_id": 1747353650,
            "msg": "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
            "pg_generator": false,
            "sensor_id": 1250383582,
            "triggered": false
          }
        }
      }`

func TestUnwrapOpenCDC_Configure(t *testing.T) {
	testCases := []struct {
		name    string
		config  config.Config
		wantErr string
	}{
		{
			name:    "invalid field",
			config:  config.Config{"field": ".Payload.Something"},
			wantErr: `invalid reference: invalid reference ".Payload.Something": unexpected field "Something": cannot resolve reference`,
		},
		{
			name:    "valid field",
			config:  config.Config{"field": ".Payload.Before"},
			wantErr: "",
		},
	}

	for _, tc := range testCases {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)

			underTest := NewOpenCDCProcessor(log.Test(t))
			gotErr := underTest.Configure(context.Background(), tc.config)
			if tc.wantErr == "" {
				is.NoErr(gotErr)
			} else {
				is.True(gotErr != nil)
				is.Equal(tc.wantErr, gotErr.Error())
			}
		})
	}
}

func TestUnwrapOpenCDC_Process(t *testing.T) {
	tests := []struct {
		name   string
		record opencdc.Record
		want   sdk.ProcessedRecord
		config config.Config
	}{
		{
			name:   "create with structured data and no payload after",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key"),
				Operation: opencdc.OperationCreate,
				Metadata:  map[string]string{},
				Payload:   opencdc.Change{},
				Position:  []byte("a position"),
			},
			want: sdk.ErrorRecord{Error: cerrors.New("field to unmarshal is nil")},
		},
		{
			name:   "create with an invalid operation",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key-raw-data"),
				Operation: opencdc.OperationCreate,
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.RawData(`{
							"position": "NzgyNjJmODUtODNmMS00ZGQwLWEyZDAtNTRmNjA1ZjkyYTg0",
							"operation": "foobar",
							"key": "MTc3NzQ5NDEtNTdhMi00MmZhLWI0MzAtODkxMmE5NDI0YjNh",
							"payload": {
							  "after": {
								"msg": "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d"
							  }
							}
						  }`,
					),
				},
				Position: []byte("test position"),
			},
			want: sdk.ErrorRecord{
				Error: cerrors.New("failed unmarshalling record: failed unmarshalling operation: invalid operation \"foobar\""),
			},
		},
		{
			name:   "create with an invalid metadata",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key-raw-data"),
				Operation: opencdc.OperationCreate,
				Payload: opencdc.Change{
					After: opencdc.RawData(`{
							"position": "NzgyNjJmODUtODNmMS00ZGQwLWEyZDAtNTRmNjA1ZjkyYTg0",
							"operation": "create",
							"metadata": "invalid",
							"key": "MTc3NzQ5NDEtNTdhMi00MmZhLWI0MzAtODkxMmE5NDI0YjNh",
							"payload": {
							  "after": {
								"msg": "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d"
							  }
							}
						  }`),
				},
				Position: []byte("test-position"),
			},
			want: sdk.ErrorRecord{
				Error: cerrors.New("failed unmarshalling record: failed unmarshalling metadata: expected a opencdc.Metadata or a map[string]interface{}, got string"),
			},
		},
		{
			name:   "create with an invalid key",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key-raw-data"),
				Operation: opencdc.OperationCreate,
				Payload: opencdc.Change{
					After: opencdc.RawData(`{
							"position": "NzgyNjJmODUtODNmMS00ZGQwLWEyZDAtNTRmNjA1ZjkyYTg0",
							"operation": "create",
							"metadata": {},
							"key": 1,
							"payload": {
							  "after": {
								"msg": "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d"
							  }
							}
						  }`),
				},
				Position: []byte("test-pos"),
			},
			want: sdk.ErrorRecord{
				Error: cerrors.New("failed unmarshalling record: failed unmarshalling key: expected a map[string]interface{} or string, got: float64"),
			},
		},
		{
			name:   "create with an invalid payload",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key-raw-data"),
				Operation: opencdc.OperationCreate,
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.RawData("this is not a json string"),
				},
				Position: []byte("test-pos"),
			},
			want: sdk.ErrorRecord{Error: cerrors.New("failed to unmarshal raw data as JSON: expected { character for map value")},
		},
		{
			name:   "create with structured data",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key"),
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"conduit.source.connector.id": "dest-log-78lpnchx7tzpyqz:source-kafka",
					"kafka.topic":                 "stream-78lpnchx7tzpyqz-generator",
					"opencdc.createdAt":           "1706028953595000000",
					"opencdc.readAt":              "1706028953606997000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"position":  []byte("NzgyNjJmODUtODNmMS00ZGQwLWEyZDAtNTRmNjA1ZjkyYTg0"),
						"operation": opencdc.OperationCreate,
						"metadata": opencdc.Metadata{
							"conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
							"opencdc.readAt":              "1706028953595546000",
							"opencdc.version":             "v1",
						},
						"key": map[string]interface{}{
							"id": "MTc3NzQ5NDEtNTdhMi00MmZhLWI0MzAtODkxMmE5NDI0YjNh",
						},
						"payload": opencdc.Change{
							Before: nil,
							After: opencdc.StructuredData{
								"event_id":     1747353650,
								"msg":          "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
								"pg_generator": false,
								"sensor_id":    1250383582,
								"triggered":    false,
							},
						},
					},
				},
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationCreate,
				Metadata: opencdc.Metadata{
					"conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
					"opencdc.readAt":              "1706028953595546000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"event_id":     1747353650,
						"msg":          "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
						"pg_generator": false,
						"sensor_id":    1250383582,
						"triggered":    false,
					},
				},
				Key:      opencdc.StructuredData{"id": "MTc3NzQ5NDEtNTdhMi00MmZhLWI0MzAtODkxMmE5NDI0YjNh"},
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
		},
		{
			name:   "create with raw data",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key-raw-data"),
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"conduit.source.connector.id": "dest-log-78lpnchx7tzpyqz:source-kafka",
					"kafka.topic":                 "stream-78lpnchx7tzpyqz-generator",
					"opencdc.createdAt":           "1706028953595000000",
					"opencdc.readAt":              "1706028953606997000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.RawData(RecordCreate),
				},
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationCreate,
				Metadata: opencdc.Metadata{
					"conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
					"opencdc.readAt":              "1706028953595546000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"event_id":     float64(1747353650),
						"msg":          "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
						"pg_generator": false,
						"sensor_id":    float64(1250383582),
						"triggered":    false,
					},
				},
				Key:      opencdc.RawData("17774941-57a2-42fa-b430-8912a9424b3a"),
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
		},
		{
			name:   "delete with before and with raw data",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key-raw-data"),
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"conduit.source.connector.id": "dest-log-78lpnchx7tzpyqz:source-kafka",
					"kafka.topic":                 "stream-78lpnchx7tzpyqz-generator",
					"opencdc.createdAt":           "1706028953595000000",
					"opencdc.readAt":              "1706028953606997000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.RawData(RecordDeleteWithBefore),
				},
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationDelete,
				Metadata: opencdc.Metadata{
					"conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
					"opencdc.readAt":              "1706028953595546000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: opencdc.StructuredData{
						"event_id":     float64(1747353650),
						"msg":          "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
						"pg_generator": false,
						"sensor_id":    float64(1250383582),
						"triggered":    false,
					},
					After: nil,
				},
				Key:      opencdc.RawData("17774941-57a2-42fa-b430-8912a9424b3a"),
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
		},
		{
			name:   "delete without before and with raw data",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key-raw-data"),
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"conduit.source.connector.id": "dest-log-78lpnchx7tzpyqz:source-kafka",
					"kafka.topic":                 "stream-78lpnchx7tzpyqz-generator",
					"opencdc.createdAt":           "1706028953595000000",
					"opencdc.readAt":              "1706028953606997000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.RawData(RecordDeleteNoBefore),
				},
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationDelete,
				Metadata: opencdc.Metadata{
					"conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
					"opencdc.readAt":              "1706028953595546000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After:  nil,
				},
				Key:      opencdc.RawData("17774941-57a2-42fa-b430-8912a9424b3a"),
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
		},
		{
			name:   "update with before and with raw data",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key-raw-data"),
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"conduit.source.connector.id": "dest-log-78lpnchx7tzpyqz:source-kafka",
					"kafka.topic":                 "stream-78lpnchx7tzpyqz-generator",
					"opencdc.createdAt":           "1706028953595000000",
					"opencdc.readAt":              "1706028953606997000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.RawData(RecordUpdateWithBefore),
				},
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationUpdate,
				Metadata: opencdc.Metadata{
					"conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
					"opencdc.readAt":              "1706028953595546000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: opencdc.StructuredData{
						"event_id":     float64(1747353650),
						"msg":          "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
						"pg_generator": false,
						"sensor_id":    float64(1250383582),
						"triggered":    false,
					},
					After: opencdc.StructuredData{
						"event_id":     float64(1.747353658e+09),
						"msg":          "string 0f5397c9-31f1-422a-9c9a-26e3574a5c31",
						"pg_generator": false,
						"sensor_id":    float64(1.25038358e+09),
						"triggered":    false,
					},
				},
				Key:      opencdc.RawData("17774941-57a2-42fa-b430-8912a9424b3a"),
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
		},
		{
			name:   "update without before and with raw data",
			config: config.Config{},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key-raw-data"),
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"conduit.source.connector.id": "dest-log-78lpnchx7tzpyqz:source-kafka",
					"kafka.topic":                 "stream-78lpnchx7tzpyqz-generator",
					"opencdc.createdAt":           "1706028953595000000",
					"opencdc.readAt":              "1706028953606997000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After:  opencdc.RawData(RecordUpdateNoBefore),
				},
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationUpdate,
				Metadata: opencdc.Metadata{
					"conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
					"opencdc.readAt":              "1706028953595546000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"event_id":     float64(1747353650),
						"msg":          "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
						"pg_generator": false,
						"sensor_id":    float64(1250383582),
						"triggered":    false,
					},
				},
				Key:      opencdc.RawData("17774941-57a2-42fa-b430-8912a9424b3a"),
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
		},
		{
			name:   "update without before and with raw data",
			config: config.Config{"field": ".Payload.After.nested"},
			record: opencdc.Record{
				Key:       opencdc.RawData("one-key-raw-data"),
				Operation: opencdc.OperationCreate,
				Metadata: map[string]string{
					"conduit.source.connector.id": "dest-log-78lpnchx7tzpyqz:source-kafka",
					"kafka.topic":                 "stream-78lpnchx7tzpyqz-generator",
					"opencdc.createdAt":           "1706028953595000000",
					"opencdc.readAt":              "1706028953606997000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"nested": opencdc.RawData(RecordUpdateNoBefore),
					},
				},
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
			want: sdk.SingleRecord{
				Operation: opencdc.OperationUpdate,
				Metadata: opencdc.Metadata{
					"conduit.source.connector.id": "source-generator-78lpnchx7tzpyqz:source",
					"opencdc.readAt":              "1706028953595546000",
					"opencdc.version":             "v1",
				},
				Payload: opencdc.Change{
					Before: nil,
					After: opencdc.StructuredData{
						"event_id":     float64(1747353650),
						"msg":          "string 0e8955b3-7fb5-4dda-8064-e10dc007f00d",
						"pg_generator": false,
						"sensor_id":    float64(1250383582),
						"triggered":    false,
					},
				},
				Key:      opencdc.RawData("17774941-57a2-42fa-b430-8912a9424b3a"),
				Position: []byte("eyJHcm91cElEIjoiNGQ2ZTBhMjktNzAwZi00Yjk4LWEzY2MtZWUyNzZhZTc4MjVjIiwiVG9waWMiOiJzdHJlYW0tNzhscG5jaHg3dHpweXF6LWdlbmVyYXRvciIsIlBhcnRpdGlvbiI6MCwiT2Zmc2V0IjoyMjF9"),
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()

			underTest := NewOpenCDCProcessor(log.Test(t))
			err := underTest.Configure(ctx, tt.config)
			is.NoErr(err)

			got := underTest.Process(ctx, []opencdc.Record{tt.record})
			is.Equal(1, len(got))
			is.Equal("", cmp.Diff(tt.want, got[0], internal.CmpProcessedRecordOpts...))
		})
	}
}

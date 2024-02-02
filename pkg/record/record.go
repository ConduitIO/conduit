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

//go:generate stringer -type=Operation -linecomment

package record

import (
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"

	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/record/schema"
)

const (
	OperationCreate   Operation = iota + 1 // create
	OperationUpdate                        // update
	OperationDelete                        // delete
	OperationSnapshot                      // snapshot
)

// Operation defines what triggered the creation of a record.
type Operation int

func (i Operation) MarshalText() ([]byte, error) {
	return []byte(i.String()), nil
}

func (i *Operation) UnmarshalText(b []byte) error {
	if len(b) == 0 {
		return nil // empty string, do nothing
	}

	switch string(b) {
	case OperationCreate.String():
		*i = OperationCreate
	case OperationUpdate.String():
		*i = OperationUpdate
	case OperationDelete.String():
		*i = OperationDelete
	case OperationSnapshot.String():
		*i = OperationSnapshot
	default:
		// it's not a known operation, but we also allow Operation(int)
		valIntRaw := strings.TrimSuffix(strings.TrimPrefix(string(b), "Operation("), ")")
		valInt, err := strconv.Atoi(valIntRaw)
		if err != nil {
			return cerrors.Errorf("unknown operation %q", b)
		}
		*i = Operation(valInt)
	}

	return nil
}

// Record represents a single data record produced by a source and/or consumed
// by a destination connector.
type Record struct {
	// Position uniquely represents the record.
	Position Position `json:"position"`
	// Operation defines what triggered the creation of a record. There are four
	// possibilities: create, update, delete or snapshot. The first three
	// operations are encountered during normal CDC operation, while "snapshot"
	// is meant to represent records during an initial load. Depending on the
	// operation, the record will contain either the payload before the change,
	// after the change, or both (see field Payload).
	Operation Operation `json:"operation"`
	// Metadata contains additional information regarding the record.
	Metadata Metadata `json:"metadata"`

	// Key represents a value that should identify the entity (e.g. database
	// row).
	Key Data `json:"key"`
	// Payload holds the payload change (data before and after the operation
	// occurred).
	Payload Change `json:"payload"`
}

// Bytes returns the JSON encoding of the Record.
func (r Record) Bytes() []byte {
	if r.Metadata == nil {
		// since we are dealing with a Record value this will not be seen
		// outside this function
		r.Metadata = make(map[string]string)
	}

	// before encoding the record set the opencdc version metadata field
	r.Metadata.SetOpenCDCVersion()
	// we don't want to mutate the metadata permanently, so we revert it
	// when we are done
	defer func() {
		delete(r.Metadata, MetadataOpenCDCVersion)
	}()

	b, err := json.Marshal(r)
	if err != nil {
		// Unlikely to happen, we receive content from a plugin through GRPC.
		// If the content could be marshaled as protobuf it can be as JSON.
		panic(fmt.Errorf("error while marshaling Entity as JSON: %w", err))
	}
	return b
}

func (r Record) Map() map[string]interface{} {
	var genericMetadata map[string]interface{}
	if r.Metadata != nil {
		genericMetadata = make(map[string]interface{}, len(r.Metadata))
		for k, v := range r.Metadata {
			genericMetadata[k] = v
		}
	}

	return map[string]any{
		"position":  []byte(r.Position),
		"operation": r.Operation.String(),
		"metadata":  genericMetadata,
		"key":       r.mapData(r.Key),
		"payload": map[string]interface{}{
			"before": r.mapData(r.Payload.Before),
			"after":  r.mapData(r.Payload.After),
		},
	}
}

func (r Record) mapData(d Data) interface{} {
	switch d := d.(type) {
	case StructuredData:
		return map[string]interface{}(d)
	case RawData:
		return d.Raw
	}
	return nil
}

func (r Record) Clone() Record {
	var (
		metadata      map[string]string
		key           Data
		payloadBefore Data
		payloadAfter  Data
	)

	if r.Metadata != nil {
		metadata = make(map[string]string, len(r.Metadata))
		for k, v := range r.Metadata {
			metadata[k] = v
		}
	}

	if r.Key != nil {
		key = r.Key.Clone()
	}
	if r.Payload.Before != nil {
		payloadBefore = r.Payload.Before.Clone()
	}
	if r.Payload.After != nil {
		payloadAfter = r.Payload.After.Clone()
	}

	clone := Record{
		Position:  bytes.Clone(r.Position),
		Operation: r.Operation,
		Metadata:  metadata,
		Key:       key,
		Payload: Change{
			Before: payloadBefore,
			After:  payloadAfter,
		},
	}
	return clone
}

func (r Record) ToOpenCDC() opencdc.Record {
	return opencdc.Record{
		Position:  opencdc.Position(r.Position),
		Operation: opencdc.Operation(r.Operation),
		Metadata:  opencdc.Metadata(r.Metadata),
		Key:       toOpenCDCData(r.Key),
		Payload: opencdc.Change{
			Before: toOpenCDCData(r.Payload.Before),
			After:  toOpenCDCData(r.Payload.After),
		},
	}
}

func toOpenCDCData(data Data) opencdc.Data {
	switch v := data.(type) {
	case nil:
		return nil
	case RawData:
		return opencdc.RawData(v.Bytes())
	case StructuredData:
		return opencdc.StructuredData(v)
	default:
		// this shouldn't happen, we hope
		panic(cerrors.Errorf("unrecognized data type: %T", v))
	}
}

type Metadata map[string]string

type Change struct {
	// Before contains the data before the operation occurred. This field is
	// optional and should only be populated for operations OperationUpdate
	// OperationDelete (if the system supports fetching the data before the
	// operation).
	Before Data `json:"before"`
	// After contains the data after the operation occurred. This field should
	// be populated for all operations except OperationDelete.
	After Data `json:"after"`
}

// Position is a unique identifier for a record being process.
// It's a Source's responsibility to choose and assign record positions,
// as they will be used by the Source in subsequent pipeline runs.
type Position []byte

// String is used when displaying the position in logs.
func (p Position) String() string {
	if p != nil {
		return string(p)
	}
	return "<nil>"
}

// Data is a structure that contains some bytes. The only structs implementing
// Data are RawData and StructuredData.
type Data interface {
	Bytes() []byte
	Clone() Data
}

// StructuredData contains data in form of a map with string keys and arbitrary
// values.
type StructuredData map[string]interface{}

func (d StructuredData) Bytes() []byte {
	b, err := json.Marshal(d)
	if err != nil {
		// Unlikely to happen, we receive content from a plugin through GRPC.
		// If the content could be marshaled as protobuf it can be as JSON.
		panic(cerrors.Errorf("StructuredData error while marshaling as JSON: %w", err))
	}
	return b
}

func (d StructuredData) Clone() Data {
	cloned := make(map[string]any, len(d))
	for k, v := range d {
		if vmap, ok := v.(map[string]any); ok {
			cloned[k] = StructuredData(vmap).Clone()
		} else {
			cloned[k] = v
		}
	}
	return StructuredData(cloned)
}

// RawData contains unstructured data in form of a byte slice.
type RawData struct {
	Raw    []byte
	Schema schema.Schema
}

func (d RawData) MarshalText() ([]byte, error) {
	buf := make([]byte, base64.StdEncoding.EncodedLen(len(d.Raw)))
	base64.StdEncoding.Encode(buf, d.Raw)
	return buf, nil
}

func (d *RawData) UnmarshalText() ([]byte, error) {
	return d.Raw, nil
}

func (d RawData) Bytes() []byte {
	return d.Raw
}

func (d RawData) Clone() Data {
	return RawData{
		Raw:    bytes.Clone(d.Raw),
		Schema: d.Schema, // this field is currently unused, we don't care about cloning it atm
	}
}

func FromOpenCDC(in opencdc.Record) Record {
	return Record{
		Position:  Position(in.Position),
		Operation: Operation(in.Operation),
		Metadata:  Metadata(in.Metadata),
		Key:       fromOpenCDCData(in.Key),
		Payload: Change{
			Before: fromOpenCDCData(in.Payload.Before),
			After:  fromOpenCDCData(in.Payload.After),
		},
	}
}

func fromOpenCDCData(data opencdc.Data) Data {
	switch v := data.(type) {
	case nil:
		return nil
	case opencdc.RawData:
		return RawData{Raw: v.Bytes()}
	case opencdc.StructuredData:
		return StructuredData(v)
	default:
		// this shouldn't happen, we hope
		panic(cerrors.Errorf("unrecognized data type: %T", v))
	}
}

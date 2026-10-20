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
	"github.com/conduitio/conduit-commons/opencdc"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
)

// Metadata keys of the record shape between the Postgres source's debezium
// mode and this processor (design: "Record shape between source and
// processor"). The source writes these; the processor reads them.
const (
	metaTopic       = "debezium.topic"
	metaOp          = "debezium.op"
	metaDescriptor  = "debezium.descriptor"
	metaOldKey      = "debezium.old_key"
	metaSourcePfx   = "debezium.source."
	metaTxID        = "debezium.tx.id"
	metaTxTotal     = "debezium.tx.total_order"
	metaTxCollOrder = "debezium.tx.data_collection_order"

	// metaPlanVersion marks a record whose Payload.After is a message-plan
	// frame, and the frame version. The Kafka destination reads it.
	metaPlanVersion = "kafka.messages.version"
	planVersion     = "1"

	// metaCollection is the OpenCDC collection metadata key.
	metaCollection = opencdc.MetadataCollection
)

// source field names, in the order Debezium writes them.
const (
	srcVersion   = "version"
	srcConnector = "connector"
	srcName      = "name"
	srcTSMs      = "ts_ms"
	srcSnapshot  = "snapshot"
	srcDB        = "db"
	srcSequence  = "sequence"
	srcTSUs      = "ts_us"
	srcTSNs      = "ts_ns"
	srcSchema    = "schema"
	srcTable     = "table"
	srcTxID      = "txId"
	srcLSN       = "lsn"
	srcXmin      = "xmin"
	srcOrigin    = "origin"
	srcOriginLSN = "origin_lsn"
)

// newCoded builds a coded error with a suggestion, keeping the construction
// to conduiterr.New as required. It returns the error interface so callers
// (and the forbidigo rule) never name the ConduitError type directly.
func newCoded(code conduiterr.Code, msg, suggestion string) error {
	e := conduiterr.New(code, msg)
	e.Suggestion = suggestion
	return e
}

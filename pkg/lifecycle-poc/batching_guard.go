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

package lifecycle

import (
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning/config"
)

// batchingOffender is one DLQ whose sdk.batch.size the v2 engine cannot run.
// Regular destinations are no longer refused: as of the funnel
// destination-batching change they batch in the engine itself
// (docs/design-documents/20261017-funnel-destination-batching.md). The DLQ is
// not wired through that path, so its SDK destination batching still withholds
// acks and stalls the stop-and-wait loop, and is refused.
type batchingOffender struct {
	label      string
	configPath string // JSON pointer into the pipeline config document
	value      string // the offending value, trimmed
}

// jsonPointerEscape escapes a reference token per RFC 6901.
func jsonPointerEscape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// dlqSettingPointer is the pointer to a dead-letter-queue setting in the
// pipeline config document.
func dlqSettingPointer(key string) string {
	return "/dead-letter-queue/settings/" + jsonPointerEscape(key)
}

func batchingOffenders(dlq map[string]string) []batchingOffender {
	var offenders []batchingOffender
	if key, value, bad := config.SDKBatchSizeAboveOne(dlq); bad {
		offenders = append(offenders, batchingOffender{
			label:      "the dead letter queue",
			configPath: dlqSettingPointer(key),
			value:      value,
		})
	}
	return offenders
}

// checkDLQBatching refuses a pipeline that would run on arch v2 with a DLQ
// connector using sdk.batch.size > 1 (#2979). It only reads engine config, so
// Start calls it before any connector is built or opened: nothing is read from
// the source and nothing is acked on the refusal path (invariants 1 and 3).
func (s *Service) checkDLQBatching(pl *pipeline.Instance) error {
	if offenders := batchingOffenders(pl.DLQ.Settings); len(offenders) > 0 {
		// An API-created pipeline has no config document, so a pointer into one
		// (and a structured fix addressing it) would address nothing.
		return errDLQBatchingUnsupported(pl.ID, offenders, pl.ProvisionedBy != pipeline.ProvisionTypeAPI)
	}
	return nil
}

// PreflightPipelineConfig applies the same refusal to a DESIRED pipeline config
// before it is applied. pkg/provisioning calls it (through an optional
// interface) ahead of a live apply's StopAndWait, so an apply that adds
// sdk.batch.size to a running pipeline's DLQ is refused with the coded error
// while the pipeline is still running and the stored config is untouched (#2979).
func (s *Service) PreflightPipelineConfig(cfg config.Pipeline) error {
	if offenders := batchingOffenders(cfg.DLQ.Settings); len(offenders) > 0 {
		return errDLQBatchingUnsupported(cfg.ID, offenders, true)
	}
	return nil
}

// errDLQBatchingUnsupported builds the refusal. withDocument is false for an
// API-created pipeline, which has no config document: ConfigPath and Fix are
// then omitted and the message alone names the setting.
func errDLQBatchingUnsupported(pipelineID string, offenders []batchingOffender, withDocument bool) error {
	parts := make([]string, len(offenders))
	for i, o := range offenders {
		if withDocument {
			parts[i] = fmt.Sprintf("%s sets %s=%s (%s)", o.label, config.SDKBatchSizeSetting, o.value, o.configPath)
		} else {
			parts[i] = fmt.Sprintf("%s sets %s=%s", o.label, config.SDKBatchSizeSetting, o.value)
		}
	}
	ce := conduiterr.New(
		CodeArchV2DestinationBatchingUnsupported,
		fmt.Sprintf(
			"can't start pipeline %s on pipeline architecture v2: %s; "+
				"v2 waits for every ack before the next read, and a DLQ batch larger than the records per write "+
				"withholds its acks until it flushes, so the pipeline can stall (sdk.batch.delay 0) or slow sharply",
			pipelineID, strings.Join(parts, "; ")),
	)
	ce.Suggestion = "remove sdk.batch.size (and sdk.batch.delay) from the dead-letter queue" +
		" so it writes without SDK batching, or run without --preview.pipeline-arch-v2 if the pipeline does not need record fan-out"
	if withDocument {
		ce.ConfigPath = offenders[0].configPath
		ce.Fix = &conduiterr.Fix{ConfigPath: offenders[0].configPath, Op: "remove"}
	}
	return ce
}

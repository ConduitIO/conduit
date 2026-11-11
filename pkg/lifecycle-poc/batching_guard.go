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
	"context"
	"fmt"
	"strings"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning/config"
)

// batchingOffender is one DLQ batching setting the v2 engine cannot run.
// Regular destinations are not refused: the funnel batches their writes itself
// (docs/design-documents/20261017-funnel-destination-batching.md) and
// configures their plugin not to (20261103-archv2-destination-batch-timing.md).
// The DLQ is not wired through that path, so an explicit SDK batch on it
// withholds acks and stalls the stop-and-wait loop, and is refused. That holds
// for sdk.batch.size > 1 and for sdk.batch.delay > 0: the SDK batches on a
// delay alone, with a size of its own (1000 in v0.17.2).
type batchingOffender struct {
	label      string
	setting    string // config.SDKBatchSizeSetting or config.SDKBatchDelaySetting
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
			setting:    config.SDKBatchSizeSetting,
			configPath: dlqSettingPointer(key),
			value:      value,
		})
	}
	// #3321: a delay alone turns the SDK batcher on too.
	if key, value, bad := config.SDKBatchDelayAboveZero(dlq); bad {
		offenders = append(offenders, batchingOffender{
			label:      "the dead letter queue",
			setting:    config.SDKBatchDelaySetting,
			configPath: dlqSettingPointer(key),
			value:      value,
		})
	}
	return offenders
}

// checkDLQBatching refuses a pipeline that would run on arch v2 with a DLQ
// connector using sdk.batch.size > 1 (#2979) or sdk.batch.delay > 0 (#3321). It only reads engine config, so
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
// sdk.batch.size or sdk.batch.delay to a running pipeline's DLQ is refused with the coded error
// while the pipeline is still running and the stored config is untouched (#2979).
func (s *Service) PreflightPipelineConfig(cfg config.Pipeline) error {
	var nonLiteral []batchingOffender
	for i, conn := range cfg.Connectors {
		if conn.Type != config.TypeDestination {
			continue
		}
		label := fmt.Sprintf("destination connector %q", conn.ID)
		nonLiteral = append(nonLiteral, nonLiteralOffenders(label, conn.Settings, connectorSettingPointer(i))...)
	}
	nonLiteral = append(nonLiteral, nonLiteralOffenders("the dead letter queue", cfg.DLQ.Settings, dlqSettingPointer)...)
	if len(nonLiteral) > 0 {
		return errBatchingNotLiteral(cfg.ID, nonLiteral, true)
	}
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
			parts[i] = fmt.Sprintf("%s sets %s=%s (%s)", o.label, o.setting, o.value, o.configPath)
		} else {
			parts[i] = fmt.Sprintf("%s sets %s=%s", o.label, o.setting, o.value)
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

// connectorSettingPointer returns the pointer builder for the settings of the
// connector at index i of the pipeline config document. A provisioned
// pipeline's ConnectorIDs follow the order of its config's connectors list
// (pkg/provisioning adds them in that order and rebuilds the list on any
// change), so the index of an ID there is its index in the document.
func connectorSettingPointer(i int) func(key string) string {
	return func(key string) string {
		return fmt.Sprintf("/connectors/%d/settings/%s", i, jsonPointerEscape(key))
	}
}

// nonLiteralOffenders lists the sdk.batch.* settings whose value is not a
// literal (config.SDKBatchNonLiteral, shared with the offline validate check).
// The value is reported as written: it is the reference, never its expansion.
func nonLiteralOffenders(label string, settings map[string]string, pointer func(key string) string) []batchingOffender {
	keys := config.SDKBatchNonLiteral(settings)
	offenders := make([]batchingOffender, 0, len(keys))
	for _, k := range keys {
		offenders = append(offenders, batchingOffender{
			label:      label,
			setting:    strings.TrimSpace(k),
			configPath: pointer(k),
			value:      strings.TrimSpace(settings[k]),
		})
	}
	return offenders
}

// checkBatchingLiterals refuses a pipeline that would run on arch v2 with a
// destination or DLQ sdk.batch.* value that is not a literal
// (CodeArchV2DestinationBatchingNotLiteral). The engine resolves batching from
// the stored settings while the plugin gets them with ${VAR} expanded, so a
// reference would let the plugin batch behind the engine and withhold acks.
// Like checkDLQBatching it reads only engine config, and Start calls it before
// any connector is built or opened: a refused start reads nothing and acks
// nothing (invariants 1 and 3).
func (s *Service) checkBatchingLiterals(ctx context.Context, pl *pipeline.Instance) error {
	var offenders []batchingOffender
	for i, id := range pl.ConnectorIDs {
		instance, err := s.connectors.Get(ctx, id)
		if err != nil {
			return cerrors.Errorf("could not fetch connector: %w", err)
		}
		if instance.Type != connector.TypeDestination {
			continue
		}
		instance.RLock()
		label := fmt.Sprintf("destination connector %q", instance.ID)
		if instance.Config.Name != "" && instance.Config.Name != instance.ID {
			label = fmt.Sprintf("destination connector %q (%s)", instance.ID, instance.Config.Name)
		}
		offenders = append(offenders, nonLiteralOffenders(label, instance.Config.Settings, connectorSettingPointer(i))...)
		instance.RUnlock()
	}
	offenders = append(offenders, nonLiteralOffenders("the dead letter queue", pl.DLQ.Settings, dlqSettingPointer)...)
	if len(offenders) > 0 {
		return errBatchingNotLiteral(pl.ID, offenders, pl.ProvisionedBy != pipeline.ProvisionTypeAPI)
	}
	return nil
}

// errBatchingNotLiteral builds the CodeArchV2DestinationBatchingNotLiteral
// refusal. withDocument is false for an API-created pipeline (no config
// document): ConfigPath is then omitted and the message alone names the
// setting. It carries no structured Fix, because the literal to set is the
// operator's choice; the suggestion says what to do.
func errBatchingNotLiteral(pipelineID string, offenders []batchingOffender, withDocument bool) error {
	parts := make([]string, len(offenders))
	for i, o := range offenders {
		if withDocument {
			parts[i] = fmt.Sprintf("%s sets %s=%s (%s)", o.label, o.setting, o.value, o.configPath)
		} else {
			parts[i] = fmt.Sprintf("%s sets %s=%s", o.label, o.setting, o.value)
		}
	}
	ce := conduiterr.New(
		CodeArchV2DestinationBatchingNotLiteral,
		fmt.Sprintf(
			"can't start pipeline %s on pipeline architecture v2: %s; "+
				"v2 batches a destination itself and configures the connector not to batch, which it decides "+
				"from the value as written, while an environment reference is expanded only for the connector, "+
				"so the connector could batch behind the engine and withhold acks",
			pipelineID, strings.Join(parts, "; ")),
	)
	ce.Suggestion = "set sdk.batch.size and sdk.batch.delay to literal values (for example 1000 and 1s) " +
		"instead of an environment reference, or run without --preview.pipeline-arch-v2"
	if withDocument {
		ce.ConfigPath = offenders[0].configPath
	}
	return ce
}

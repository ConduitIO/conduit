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
	"sort"
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/provisioning/config"
)

// sdkBatchSizeSetting is the destination setting the conduit-connector-sdk
// reads to buffer writes. See CodeArchV2DestinationBatchingUnsupported.
const sdkBatchSizeSetting = "sdk.batch.size"

// batchingDest is one destination connector as the guard sees it, whichever
// source it came from (the engine's stored instances at Start, or a desired
// config document at live apply).
type batchingDest struct {
	index    int // position in the pipeline's connectors list, as in the config document
	userID   string
	name     string
	settings map[string]string
}

// batchingOffender is one destination (or the DLQ) whose sdk.batch.size the v2
// engine cannot run.
type batchingOffender struct {
	label      string // human description: destination connector "x" / the dead letter queue
	configPath string // JSON pointer into the pipeline config document
	value      string // the offending value, trimmed
}

// sdkBatchSizeAboveOne reports whether settings carry an sdk.batch.size the SDK
// would read as larger than 1, returning the key as written and the trimmed
// value.
//
// It mirrors how conduit-connector-sdk reads the setting: config.Sanitize trims
// every key and value, so " sdk.batch.size" is the same setting (keys are
// otherwise case-sensitive). Validation then parses the value with
// strconv.Atoi, so the guard does the same. (Decoding then uses base 0, so
// "0144" is validated as 144 but decoded as octal 100; the octal reading is
// never larger than the decimal one, so Atoi alone decides.)
// A value Atoi rejects never reaches the batcher (the plugin fails its own
// config validation), so it is left to the plugin. Unset, empty, 0, 1 and
// negative values are not refused.
func sdkBatchSizeAboveOne(settings map[string]string) (key, value string, ok bool) {
	var keys []string
	for k := range settings {
		if strings.TrimSpace(k) == sdkBatchSizeSetting {
			keys = append(keys, k)
		}
	}
	sort.Strings(keys) // deterministic when a map carries both padded and plain keys
	for _, k := range keys {
		v := strings.TrimSpace(settings[k])
		if n, err := strconv.Atoi(v); err == nil && n > 1 {
			return k, v, true
		}
	}
	return "", "", false
}

// jsonPointerEscape escapes a reference token per RFC 6901.
func jsonPointerEscape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// connectorSettingPointer is the pointer to a connector setting in the
// pipeline config document, indexed like pkg/provisioning/config's own
// validation paths ("/connectors/<n>/...").
func connectorSettingPointer(index int, key string) string {
	return fmt.Sprintf("/connectors/%d/settings/%s", index, jsonPointerEscape(key))
}

// dlqSettingPointer is the pointer to a dead-letter-queue setting in the
// pipeline config document.
func dlqSettingPointer(key string) string {
	return "/dead-letter-queue/settings/" + jsonPointerEscape(key)
}

func batchingOffenders(dests []batchingDest, dlq map[string]string) []batchingOffender {
	var offenders []batchingOffender
	for _, d := range dests {
		key, value, bad := sdkBatchSizeAboveOne(d.settings)
		if !bad {
			continue
		}
		label := fmt.Sprintf("destination connector %q", d.userID)
		if d.name != "" {
			label += fmt.Sprintf(" (name %q)", d.name)
		}
		offenders = append(offenders, batchingOffender{
			label:      label,
			configPath: connectorSettingPointer(d.index, key),
			value:      value,
		})
	}
	// The DLQ is a destination too and runs through the same stop-and-wait task.
	if key, value, bad := sdkBatchSizeAboveOne(dlq); bad {
		offenders = append(offenders, batchingOffender{
			label:      "the dead letter queue",
			configPath: dlqSettingPointer(key),
			value:      value,
		})
	}
	return offenders
}

// userConnectorID strips the pipeline prefix the provisioner adds to
// config-defined connector IDs ("<pipeline>:<id>"), giving the ID as the user
// wrote it in the pipeline config document. API-created connectors have no
// such prefix and keep their ID.
func userConnectorID(pipelineID, id string) string {
	return strings.TrimPrefix(id, pipelineID+":")
}

// checkDestinationBatching refuses a pipeline that would run on arch v2 with a
// destination or DLQ connector using sdk.batch.size > 1 (#2979). It only reads
// engine config, so Start calls it before any connector is built or opened:
// nothing is read from the source and nothing is acked on the refusal path
// (invariants 1 and 3).
func (s *Service) checkDestinationBatching(ctx context.Context, pl *pipeline.Instance) error {
	var dests []batchingDest

	for i, connID := range pl.ConnectorIDs {
		instance, err := s.connectors.Get(ctx, connID)
		if err != nil {
			// Not ours to report: buildDestinationTasks fetches the same
			// connector and returns the real error.
			if cerrors.Is(err, connector.ErrInstanceNotFound) {
				continue
			}
			return cerrors.Errorf("could not fetch connector: %w", err)
		}
		if instance.Type != connector.TypeDestination {
			continue
		}
		instance.RLock()
		dests = append(dests, batchingDest{
			index:    i,
			userID:   userConnectorID(pl.ID, instance.ID),
			name:     instance.Config.Name,
			settings: instance.Config.Settings,
		})
		instance.RUnlock()
	}

	if offenders := batchingOffenders(dests, pl.DLQ.Settings); len(offenders) > 0 {
		// An API-created pipeline has no config document, so a pointer into one
		// (and a structured fix addressing it) would address nothing.
		return errDestinationBatchingUnsupported(pl.ID, offenders, pl.ProvisionedBy != pipeline.ProvisionTypeAPI)
	}
	return nil
}

// PreflightPipelineConfig applies the same refusal to a DESIRED pipeline config
// before it is applied. pkg/provisioning calls it (through an optional
// interface) ahead of a live apply's StopAndWait, so an apply that adds
// sdk.batch.size to a running pipeline is refused with the coded error while
// the pipeline is still running and the stored config is untouched, instead of
// stopping a healthy pipeline, committing the config, and then being refused
// at Start (#2979).
func (s *Service) PreflightPipelineConfig(cfg config.Pipeline) error {
	var dests []batchingDest
	for i, c := range cfg.Connectors {
		if c.Type != config.TypeDestination {
			continue
		}
		dests = append(dests, batchingDest{
			index:    i,
			userID:   userConnectorID(cfg.ID, c.ID),
			name:     c.Name,
			settings: c.Settings,
		})
	}
	if offenders := batchingOffenders(dests, cfg.DLQ.Settings); len(offenders) > 0 {
		return errDestinationBatchingUnsupported(cfg.ID, offenders, true)
	}
	return nil
}

// errDestinationBatchingUnsupported builds the refusal. Every offender is named
// in the message; ConfigPath and Fix address the first one, because a
// ConduitError carries a single path. Fixing it and re-running reports the next.
// withDocument is false for an API-created pipeline, which has no config
// document: ConfigPath and Fix are then omitted and the message alone names the
// connector (id and name) and the setting.
func errDestinationBatchingUnsupported(pipelineID string, offenders []batchingOffender, withDocument bool) error {
	parts := make([]string, len(offenders))
	for i, o := range offenders {
		if withDocument {
			parts[i] = fmt.Sprintf("%s sets %s=%s (%s)", o.label, sdkBatchSizeSetting, o.value, o.configPath)
		} else {
			parts[i] = fmt.Sprintf("%s sets %s=%s", o.label, sdkBatchSizeSetting, o.value)
		}
	}
	ce := conduiterr.New(
		CodeArchV2DestinationBatchingUnsupported,
		fmt.Sprintf(
			"can't start pipeline %s on pipeline architecture v2: %s; "+
				"v2 waits for every ack before the next read, and a destination batch larger than the records per write "+
				"withholds its acks until it flushes, so the pipeline can stall (sdk.batch.delay 0) or slow sharply",
			pipelineID, strings.Join(parts, "; ")),
	)
	ce.Suggestion = "remove sdk.batch.size (and sdk.batch.delay) from the destination" +
		" so it writes without SDK batching, or run without --preview.pipeline-arch-v2 if the pipeline does not need record fan-out"
	if withDocument {
		ce.ConfigPath = offenders[0].configPath
		ce.Fix = &conduiterr.Fix{ConfigPath: offenders[0].configPath, Op: "remove"}
	}
	return ce
}

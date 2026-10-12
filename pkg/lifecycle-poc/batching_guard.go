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
	"strconv"
	"strings"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
)

// sdkBatchSizeSetting is the destination setting the conduit-connector-sdk
// reads to buffer writes. See CodeArchV2DestinationBatchingUnsupported.
const sdkBatchSizeSetting = "sdk.batch.size"

// batchingOffender is one destination (or the DLQ) whose sdk.batch.size the v2
// engine cannot run.
type batchingOffender struct {
	label      string // human description: destination connector "x" / the dead letter queue
	configPath string // JSON pointer to the setting
	size       int64
}

// sdkBatchSizeAboveOne reports the destination's sdk.batch.size when it parses
// as an integer greater than 1. Anything else (unset, empty, 0, 1, negative,
// not a number) is not an offender: the settings arrive as the engine stores
// them, a string map, and a value that does not parse is the plugin's own
// config validation to reject, not ours.
func sdkBatchSizeAboveOne(settings map[string]string) (int64, bool) {
	raw, ok := settings[sdkBatchSizeSetting]
	if !ok {
		return 0, false
	}
	size, err := strconv.ParseInt(strings.TrimSpace(raw), 10, 64)
	if err != nil || size <= 1 {
		return 0, false
	}
	return size, true
}

// jsonPointerEscape escapes a reference token per RFC 6901.
func jsonPointerEscape(s string) string {
	return strings.NewReplacer("~", "~0", "/", "~1").Replace(s)
}

// checkDestinationBatching refuses a pipeline that would run on arch v2 with a
// destination or DLQ connector using sdk.batch.size > 1 (#2979). It only reads
// engine config, so Start calls it before any connector is built or opened:
// nothing is read from the source and nothing is acked on the refusal path
// (invariants 1 and 3).
func (s *Service) checkDestinationBatching(ctx context.Context, pl *pipeline.Instance) error {
	var offenders []batchingOffender

	for _, connID := range pl.ConnectorIDs {
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
		size, bad := sdkBatchSizeAboveOne(instance.Config.Settings)
		instance.RUnlock()
		if bad {
			offenders = append(offenders, batchingOffender{
				label:      fmt.Sprintf("destination connector %q", instance.ID),
				configPath: "/connectors/" + jsonPointerEscape(instance.ID) + "/settings/" + sdkBatchSizeSetting,
				size:       size,
			})
		}
	}

	// The DLQ is a destination too and runs through the same stop-and-wait task.
	if size, bad := sdkBatchSizeAboveOne(pl.DLQ.Settings); bad {
		offenders = append(offenders, batchingOffender{
			label:      "the dead letter queue",
			configPath: "/dead-letter-queue/settings/" + sdkBatchSizeSetting,
			size:       size,
		})
	}

	if len(offenders) == 0 {
		return nil
	}
	return errDestinationBatchingUnsupported(pl, offenders)
}

func errDestinationBatchingUnsupported(pl *pipeline.Instance, offenders []batchingOffender) error {
	parts := make([]string, len(offenders))
	for i, o := range offenders {
		parts[i] = fmt.Sprintf("%s sets %s=%d (%s)", o.label, sdkBatchSizeSetting, o.size, o.configPath)
	}
	ce := conduiterr.New(
		CodeArchV2DestinationBatchingUnsupported,
		fmt.Sprintf(
			"can't start pipeline %s on pipeline architecture v2: %s; "+
				"v2 waits for every ack before the next read, and an SDK-batching destination only acks when its batch flushes, "+
				"so the pipeline would stall (sdk.batch.delay 0) or run more than 99%% slower",
			pl.ID, strings.Join(parts, "; ")),
	)
	ce.ConfigPath = offenders[0].configPath
	ce.Suggestion = "remove sdk.batch.size (and sdk.batch.delay) from the destination" +
		" so it writes without SDK batching, or run without --preview.pipeline-arch-v2 if the pipeline does not need record fan-out"
	ce.Fix = &conduiterr.Fix{ConfigPath: offenders[0].configPath, Op: "remove"}
	return ce
}

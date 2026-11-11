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
	"strconv"
	"time"

	commonsconfig "github.com/conduitio/conduit-commons/config"
	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/conduitio/conduit/pkg/provisioning/config"
)

// batchOrigin says where the effective value of one sdk.batch.* key came
// from. The values are the strings the build-time INFO log reports.
type batchOrigin string

const (
	// batchOriginNone: neither the pipeline config nor the connector spec
	// gives the key a value above 0, so it is 0 (off).
	batchOriginNone batchOrigin = "none"
	// batchOriginPipeline: the pipeline config sets the key to a value that
	// parses.
	batchOriginPipeline batchOrigin = "pipeline_config"
	// batchOriginConnector: the connector spec's default for the key, when
	// it is above 0.
	batchOriginConnector batchOrigin = "connector_default"
)

// destinationBatching is a destination's effective SDK batching, resolved per
// key, and the keys the plugin must be configured with "0" for.
type destinationBatching struct {
	size        int
	sizeOrigin  batchOrigin
	delay       time.Duration
	delayOrigin batchOrigin
	// overrides are the keys (config.SDKBatchSizeSetting,
	// config.SDKBatchDelaySetting) whose effective value would turn the
	// plugin's batcher on; see connector.Destination.SetPluginBatchingOverrides.
	overrides []string
}

// enabled reports whether the effective settings batch, with the connector
// SDK's predicate (size > 1 or delay > 0).
func (b destinationBatching) enabled() bool { return b.size > 1 || b.delay > 0 }

// fromConnectorDefault reports whether any effective value came from the
// connector spec rather than the pipeline config.
func (b destinationBatching) fromConnectorDefault() bool {
	return b.sizeOrigin == batchOriginConnector || b.delayOrigin == batchOriginConnector
}

// resolveDestinationBatching resolves the effective sdk.batch.size and
// sdk.batch.delay of a destination (docs/design-documents/
// 20261103-archv2-destination-batch-timing.md, Decision 2). Per key:
//
//  1. the pipeline config value, if the key is present (matched after
//     trimming, a non-empty value) and parses (strconv.Atoi,
//     time.ParseDuration), as the connector SDK reads it;
//  2. otherwise the default in the plugin's destination spec, if the spec
//     declares the key;
//  3. otherwise 0.
//
// A key is listed in overrides when the spec declares it and its effective
// value would turn the plugin's batcher on (size > 1, delay > 0), so the
// engine batches with that value and the plugin is configured with "0". A
// pipeline value that does not parse, or parses below 0, is never overridden:
// it reaches the plugin unchanged, which rejects it in Configure as it does
// today. A key the spec does not declare is never added, because SDK plugins
// reject unrecognised settings.
//
// haveSpec is false when the plugin's specification is not known (a dispenser
// built outside the plugin registries, such as a test double). Then a key
// counts as declared only when the pipeline config sets it: the override
// replaces a value the plugin receives anyway and never adds a key, which
// keeps the plugin's batcher off as deleting the key used to.
func resolveDestinationBatching(settings map[string]string, params commonsconfig.Parameters, haveSpec bool) destinationBatching {
	var b destinationBatching
	var sizeOverride, delayOverride bool

	b.size, b.sizeOrigin, sizeOverride = resolveBatchKey(settings, params, haveSpec, config.SDKBatchSizeSetting,
		func(v string) (int, bool) {
			n, err := strconv.Atoi(v)
			return n, err == nil
		},
		func(n int) bool { return n > 1 },
	)
	b.delay, b.delayOrigin, delayOverride = resolveBatchKey(settings, params, haveSpec, config.SDKBatchDelaySetting,
		func(v string) (time.Duration, bool) {
			d, err := time.ParseDuration(v)
			return d, err == nil
		},
		func(d time.Duration) bool { return d > 0 },
	)
	if b.size < 0 {
		b.size = 0 // negative: left to the plugin to reject, contributes nothing here
	}
	if b.delay < 0 {
		b.delay = 0
	}
	if sizeOverride {
		b.overrides = append(b.overrides, config.SDKBatchSizeSetting)
	}
	if delayOverride {
		b.overrides = append(b.overrides, config.SDKBatchDelaySetting)
	}
	return b
}

// resolveBatchKey resolves one key for resolveDestinationBatching. parse
// reports whether a value parses; enables is the SDK's "turns the batcher on"
// test for the key.
func resolveBatchKey[T int | time.Duration](
	settings map[string]string,
	params commonsconfig.Parameters,
	haveSpec bool,
	key string,
	parse func(string) (T, bool),
	enables func(T) bool,
) (value T, origin batchOrigin, override bool) {
	raw, present := config.SDKSettingValue(settings, key)
	param, declared := params[key]
	if !haveSpec {
		declared = present
	}

	if present {
		v, ok := parse(raw)
		switch {
		case ok && v >= 0:
			return v, batchOriginPipeline, declared && enables(v)
		case ok:
			// Negative: the plugin rejects it (validate:"gt=-1"). Pass it
			// through; the engine does not hide a config error.
			return v, batchOriginPipeline, false
		}
		// Does not parse: passed through for the plugin to reject. The engine
		// falls back to the spec default below, but never overrides the key.
		if d, ok := parse(param.Default); haveSpec && declared && ok && d > 0 {
			return d, batchOriginConnector, false
		}
		var zero T
		return zero, batchOriginNone, false
	}

	if haveSpec && declared {
		if d, ok := parse(param.Default); ok && d > 0 {
			return d, batchOriginConnector, enables(d)
		}
	}
	var zero T
	return zero, batchOriginNone, false
}

// logBatchingFromConnectorDefault reports, once per destination at build time,
// that the engine took over batching the connector's spec enables by default
// (#3321), and where each effective value came from.
func logBatchingFromConnectorDefault(ctx context.Context, logger log.CtxLogger, instance *connector.Instance, b destinationBatching) {
	logger.Info(ctx).
		Str(log.ConnectorIDField, instance.ID).
		Str("plugin", instance.Plugin).
		Int("sdk_batch_size", b.size).
		Str("sdk_batch_size_origin", string(b.sizeOrigin)).
		Dur("sdk_batch_delay", b.delay).
		Str("sdk_batch_delay_origin", string(b.delayOrigin)).
		Strs("plugin_settings_set_to_zero", b.overrides).
		Msg("destination batching taken over from connector defaults")
}

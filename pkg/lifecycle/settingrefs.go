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
	"strings"

	"github.com/conduitio/conduit/pkg/connector"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/pipeline"
	"github.com/conduitio/conduit/pkg/processor"
)

// settingRefsFix is the operator-facing remedy, shared by the message and the
// error's Suggestion.
const settingRefsFix = "this database was written by Conduit v0.21 or later; run v0.21 or later, or re-provision the pipeline from its file"

// SettingRefsConnectors fetches connector instances for CheckSettingRefs.
type SettingRefsConnectors interface {
	Get(ctx context.Context, id string) (*connector.Instance, error)
}

// SettingRefsProcessors fetches processor instances for CheckSettingRefs.
type SettingRefsProcessors interface {
	Get(ctx context.Context, id string) (*processor.Instance, error)
}

type settingRefsOffender struct {
	label string
	path  string // JSON pointer into the pipeline config document
	keys  []string
}

// CheckSettingRefs refuses a pipeline whose stored connector, processor or DLQ
// record carries SettingRefs, a field that only Conduit v0.21 and later write.
// It returns an error coded pipeline.CodeSettingsReferencesUnsupported naming
// the pipeline, the entity, the config path and the fix, or nil.
//
// It reads stored config only, so callers run it before anything is built or
// opened: nothing is read from the source and nothing is acked on the refusal
// path (Invariants 1 and 3). It never resolves or reports the template values,
// only the setting names.
//
// Only Config is enforced. A LastActiveConfig that still carries SettingRefs
// after Config was re-provisioned is stale history, and refusing on it would
// make a successful reconcile (which replaces Config) unable to heal the
// pipeline; the field is still preserved on every re-write (Invariant 6).
func CheckSettingRefs(ctx context.Context, pl *pipeline.Instance, conns SettingRefsConnectors, procs SettingRefsProcessors) error {
	var offenders []settingRefsOffender

	checkProcs := func(prefix string, ids []string) error {
		for j, id := range ids {
			p, err := procs.Get(ctx, id)
			if err != nil {
				if cerrors.Is(err, processor.ErrInstanceNotFound) {
					continue // the build reports the missing processor
				}
				return cerrors.Errorf("could not fetch processor: %w", err)
			}
			if p.Config.HasSettingRefs() {
				offenders = append(offenders, settingRefsOffender{
					label: fmt.Sprintf("processor %q", p.ID),
					path:  fmt.Sprintf("%s/processors/%d/settings", prefix, j),
					keys:  sortedKeys(p.Config.SettingRefs),
				})
			}
		}
		return nil
	}

	for i, connID := range pl.ConnectorIDs {
		c, err := conns.Get(ctx, connID)
		if err != nil {
			if cerrors.Is(err, connector.ErrInstanceNotFound) {
				continue // the build reports the missing connector
			}
			return cerrors.Errorf("could not fetch connector: %w", err)
		}
		c.RLock()
		cfg, procIDs := c.Config, c.ProcessorIDs
		c.RUnlock()
		prefix := fmt.Sprintf("/connectors/%d", i)
		if cfg.HasSettingRefs() {
			offenders = append(offenders, settingRefsOffender{
				label: fmt.Sprintf("connector %q (name %q)", strings.TrimPrefix(connID, pl.ID+":"), cfg.Name),
				path:  prefix + "/settings",
				keys:  sortedKeys(cfg.SettingRefs),
			})
		}
		if err := checkProcs(prefix, procIDs); err != nil {
			return err
		}
	}
	if err := checkProcs("", pl.ProcessorIDs); err != nil {
		return err
	}
	if pl.DLQ.HasSettingRefs() {
		offenders = append(offenders, settingRefsOffender{
			label: "the dead letter queue",
			path:  "/dead-letter-queue/settings",
			keys:  sortedKeys(pl.DLQ.SettingRefs),
		})
	}
	if len(offenders) == 0 {
		return nil
	}

	parts := make([]string, len(offenders))
	for i, o := range offenders {
		parts[i] = fmt.Sprintf("%s has settings stored as references (%s: %s)", o.label, o.path, strings.Join(o.keys, ", "))
	}
	ce := conduiterr.New(
		pipeline.CodeSettingsReferencesUnsupported,
		fmt.Sprintf("can't start pipeline %q (%s): %s; %s", pl.Config.Name, pl.ID, strings.Join(parts, "; "), settingRefsFix),
	)
	ce.Suggestion = settingRefsFix
	ce.ConfigPath = offenders[0].path
	return ce
}

func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

// refuseSettingRefs records the refusal: it marks the pipeline Degraded with
// the coded message so the API, CLI and UI show the cause. The caller holds
// the pipeline's start reservation, so no run exists whose status this could
// overwrite. The stored connector, processor and DLQ records are not touched
// (Invariant 6).
func (s *Service) refuseSettingRefs(ctx context.Context, pl *pipeline.Instance, cause error) {
	s.logger.Warn(ctx).
		Err(cause).
		Str("pipeline_id", pl.ID).
		Str("code", pipeline.CodeSettingsReferencesUnsupported.Reason()).
		Msg("pipeline refused: stored settings use references written by a newer Conduit")

	unlock := s.lockStatus(pl.ID)
	defer unlock()
	wctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), s.runningWriteTimeout())
	defer cancel()
	if err := s.pipelines.UpdateStatus(wctx, pl.ID, pipeline.StatusDegraded,
		pipeline.CodeSettingsReferencesUnsupported.Reason()+": "+cause.Error()); err != nil {
		s.logStatusNotPersisted(ctx, pl.ID, pipeline.StatusDegraded, err)
	}
}

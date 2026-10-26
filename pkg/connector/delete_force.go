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

package connector

import (
	"context"
	"fmt"
	"strings"
	"sync"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/metrics/measure"
)

type (
	forceDeleteKey   struct{}
	deleteWarningKey struct{}
)

// WithForceDelete returns a context that lets a delete go ahead although the
// connector's stored settings no longer resolve, skipping the plugin's deletion
// hook. The caller asked for it explicitly (the API's force flag).
func WithForceDelete(ctx context.Context) context.Context {
	return context.WithValue(ctx, forceDeleteKey{}, true)
}

// ForceDelete reports whether ctx carries [WithForceDelete].
func ForceDelete(ctx context.Context) bool {
	v, _ := ctx.Value(forceDeleteKey{}).(bool)
	return v
}

// DeleteWarnings collects what a forced delete skipped, for the response.
type DeleteWarnings struct {
	mu   sync.Mutex
	list []string
}

// WithDeleteWarnings returns a context that collects delete warnings and the
// collector.
func WithDeleteWarnings(ctx context.Context) (context.Context, *DeleteWarnings) {
	w := &DeleteWarnings{}
	return context.WithValue(ctx, deleteWarningKey{}, w), w
}

// List returns the warnings collected so far.
func (w *DeleteWarnings) List() []string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return append([]string(nil), w.list...)
}

func addDeleteWarning(ctx context.Context, msg string) {
	if w, ok := ctx.Value(deleteWarningKey{}).(*DeleteWarnings); ok {
		w.mu.Lock()
		w.list = append(w.list, msg)
		w.mu.Unlock()
	}
}

// checkDeleteHook runs before a connector is deleted. The plugin's "deleted"
// hook cleans up what the connector created on the other side (a Postgres
// replication slot, for example), and it needs the connector's settings. If they
// no longer resolve the hook cannot run: the delete is refused with
// connector.delete_hook_unresolvable, naming the setting and the fix, unless
// the caller forces it. A forced delete skips the hook, says so in the response
// warnings and the log, and counts in conduit_connector_force_deletes_total.
func (i *Instance) checkDeleteHook(ctx context.Context) error {
	i.RLock()
	settings := i.LastActiveConfig.Settings
	i.RUnlock()
	if settings == nil {
		return nil // never started: no hook to run
	}
	_, err := i.resolveSettings(settings)
	if err == nil {
		return nil
	}
	setting := "a setting"
	if ce, ok := conduiterr.Get(err); ok && ce.ConfigPath != "" {
		setting = ce.ConfigPath
	}
	reason := i.redactError(err).Error()
	if ForceDelete(ctx) {
		msg := fmt.Sprintf("connector %s was deleted with force: %s no longer resolves (%s), so the %s plugin's deletion hook did not run; clean up what it created on the other side by hand (for example drop a Postgres replication slot)",
			i.ID, setting, reason, i.Type)
		i.logger.Warn(ctx).Str("setting", setting).Msg(msg)
		addDeleteWarning(ctx, msg)
		measure.ConnectorForceDeletesCounter.WithValues(strings.ToLower(i.Type.String())).Inc()
		return nil
	}
	ce := conduiterr.New(CodeConnectorDeleteHookUnresolvable,
		fmt.Sprintf("connector %s cannot be deleted: %s no longer resolves (%s), so the plugin's deletion hook cannot run and what it cleans up (for example a Postgres replication slot, which keeps retaining WAL) would stay behind", i.ID, setting, reason))
	ce.ConfigPath = setting
	ce.Suggestion = "restore the setting (for example add the environment variable back to env.allow, or fix the path) and delete again; " +
		"or delete with force to skip the hook and clean up by hand (see docs/operations/connector-delete-hook.md)"
	return ce
}

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
	"sort"
	"strings"
	"time"

	"github.com/conduitio/conduit-commons/database"
	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
)

const (
	// orphanKeyPrefix is the store prefix of orphan entries. Nothing else
	// reads this prefix, and connector:instance: does not match it. Do not
	// change it without a migration: `conduit doctor` and older entries rely
	// on it.
	orphanKeyPrefix = "connector:orphan:"

	// OrphanVersion is the current version of the stored Orphan format.
	// Bump it, and teach DecodeOrphan the old one, if the format changes.
	OrphanVersion = 1
)

// Orphan records a connector that was deleted with force after its delete
// hook failed, so resources it created outside Conduit may still exist (a
// Postgres replication slot, a publication, a temporary table). It is
// written in the same store transaction that deletes the connector record,
// and stays until the operator acknowledges it (#3058).
//
// The JSON form is a persisted format: fields may be added, never renamed or
// removed without bumping OrphanVersion.
type Orphan struct {
	Version     int    `json:"version"`
	ConnectorID string `json:"connectorId"`
	Type        string `json:"type"`
	PipelineID  string `json:"pipelineId"`
	Plugin      string `json:"plugin"`
	// HookError is the delete hook's error with secret-looking setting values
	// scrubbed (log.ScrubSettingValues). It is the plugin's only channel for
	// naming what it failed to release.
	HookError string    `json:"hookError"`
	DeletedAt time.Time `json:"deletedAt"`
}

// OrphanWarning is the coded, non-fatal finding a forced delete with a
// failing hook produces (CodeConnectorOrphanedResources). It is a plain value
// rather than a ConduitError because it is never returned as an error.
type OrphanWarning struct {
	Code       conduiterr.Code
	Message    string
	Suggestion string
}

// Warning returns the warning that describes o, for API responses, logs and
// doctor findings.
func (o *Orphan) Warning() OrphanWarning {
	return OrphanWarning{
		Code: CodeConnectorOrphanedResources,
		Message: fmt.Sprintf(
			"connector %q (%s, plugin %q, pipeline %q) was deleted with force after its delete hook failed, "+
				"so resources it created outside Conduit may remain: %s",
			o.ConnectorID, o.Type, o.Plugin, o.PipelineID, o.HookError,
		),
		Suggestion: orphanSuggestion(o.ConnectorID),
	}
}

// DecodeOrphan decodes a stored orphan entry. It is exported for `conduit
// doctor`, which reads the store without a running service.
func DecodeOrphan(raw []byte) (*Orphan, error) {
	var o Orphan
	if err := json.Unmarshal(raw, &o); err != nil {
		return nil, cerrors.Errorf("failed to decode orphan entry: %w", err)
	}
	if o.Version != OrphanVersion {
		return nil, cerrors.Errorf("unsupported orphan entry version %d (this Conduit reads version %d)", o.Version, OrphanVersion)
	}
	return &o, nil
}

// ListOrphans reads every orphan entry from db, sorted by connector ID. It
// does not need a Service, so `conduit doctor` can call it against a store
// it opened itself. An entry that does not decode is returned as an error,
// not skipped: a finding about a disk-fill hazard must not vanish silently.
func ListOrphans(ctx context.Context, db database.DB) ([]*Orphan, error) {
	keys, err := db.GetKeys(ctx, orphanKeyPrefix)
	if err != nil {
		return nil, cerrors.Errorf("failed to list orphan entries: %w", err)
	}
	sort.Strings(keys)
	out := make([]*Orphan, 0, len(keys))
	for _, key := range keys {
		raw, err := db.Get(ctx, key)
		if err != nil {
			return nil, cerrors.Errorf("failed to read orphan entry %q: %w", key, err)
		}
		if len(raw) == 0 {
			continue // deleted in this transaction
		}
		o, err := DecodeOrphan(raw)
		if err != nil {
			return nil, cerrors.Errorf("orphan entry %q: %w", strings.TrimPrefix(key, orphanKeyPrefix), err)
		}
		out = append(out, o)
	}
	return out, nil
}

func (s *Store) setOrphan(ctx context.Context, o *Orphan) error {
	raw, err := json.Marshal(o)
	if err != nil {
		return cerrors.Errorf("failed to encode orphan entry for connector %q: %w", o.ConnectorID, err)
	}
	if err := s.db.Set(ctx, orphanKeyPrefix+o.ConnectorID, raw); err != nil {
		return cerrors.Errorf("failed to store orphan entry for connector %q: %w", o.ConnectorID, err)
	}
	return nil
}

func (s *Store) getOrphan(ctx context.Context, id string) (*Orphan, error) {
	raw, err := s.db.Get(ctx, orphanKeyPrefix+id)
	if err != nil {
		if cerrors.Is(err, database.ErrKeyNotExist) {
			return nil, nil
		}
		return nil, cerrors.Errorf("failed to read orphan entry for connector %q: %w", id, err)
	}
	if len(raw) == 0 {
		return nil, nil
	}
	return DecodeOrphan(raw)
}

func (s *Store) deleteOrphan(ctx context.Context, id string) error {
	if err := s.db.Set(ctx, orphanKeyPrefix+id, nil); err != nil {
		return cerrors.Errorf("failed to delete orphan entry for connector %q: %w", id, err)
	}
	return nil
}

// deleteHookError builds CodeConnectorDeleteHookFailed for instance from the
// hook's error. The plugin's error text is quoted with the connector's
// secret-looking setting values scrubbed, because ConduitError messages
// cross the API boundary.
func deleteHookError(instance *Instance, hookErr error) error {
	ce := conduiterr.Wrap(CodeConnectorDeleteHookFailed, fmt.Sprintf(
		"could not delete connector %q (%s, plugin %q, pipeline %q): its delete hook failed, "+
			"so resources it created outside Conduit may not have been released: %s",
		instance.ID, strings.ToLower(instance.Type.String()), instance.Plugin, instance.PipelineID,
		scrubbedHookError(instance, hookErr),
	), hookErr)
	ce.Suggestion = deleteHookSuggestion
	return ce
}

// scrubbedHookError is hookErr's text with the values of the connector's
// secret-looking settings (current and last active) replaced.
func scrubbedHookError(instance *Instance, hookErr error) string {
	msg := hookErr.Error()
	msg = log.ScrubSettingValues(msg, instance.LastActiveConfig.Settings)
	msg = log.ScrubSettingValues(msg, instance.Config.Settings)
	return msg
}

func orphanSuggestion(connectorID string) string {
	return "check the external system for resources this connector created (the plugin's error names what it " +
		"failed to release) and remove them by hand, then run " +
		fmt.Sprintf("'conduit connectors delete %s --acknowledge-orphan' ", connectorID) +
		"(see docs/operations/connector-delete-hook-failures.md)"
}

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

package emit

import (
	"regexp"
	"sort"
	"strings"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
)

// Kind applies one decision to a plan. It sets the entry's target, and may
// turn the entry into a finding (for example a conflict). A returned error
// is a bug in the kind or in the rule data and fails the run as an internal
// error.
type Kind func(p *Plan, d *rules.Decision) error

// Kind names.
const (
	KindNone    = "none"
	KindSetting = "setting"
)

var kinds = map[string]Kind{
	KindNone:    func(*Plan, *rules.Decision) error { return nil },
	KindSetting: setting,
}

// Kinds returns the registered kind names, sorted. A test checks that every
// kind the embedded rule data names is registered.
func Kinds() []string {
	out := make([]string, 0, len(kinds))
	for k := range kinds {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}

// Apply applies every decision that has an emit action to p, in order.
func Apply(p *Plan, ds []rules.Decision) error {
	for i := range ds {
		if err := apply(p, &ds[i], false); err != nil {
			return err
		}
	}
	return nil
}

// ApplyDefaults applies worker decisions to p: a worker setting is inherited
// only where the connector did not set it. ds is not modified, since one
// worker setting lands in many pipelines; the entries as applied to p are
// returned, index-aligned with ds, so the caller can record a downgrade (a
// worker value that cannot be emitted) on the worker's own entry. Dropping
// the refined copy would let a pipeline run without the setting it needed.
func ApplyDefaults(p *Plan, ds []rules.Decision) ([]kcmigrate.Entry, error) {
	out := make([]kcmigrate.Entry, len(ds))
	for i := range ds {
		d := ds[i]
		if err := apply(p, &d, true); err != nil {
			return nil, err
		}
		out[i] = d.Entry
	}
	return out, nil
}

func apply(p *Plan, d *rules.Decision, onlyUnset bool) error {
	if d.Emit == nil {
		return nil
	}
	k, ok := kinds[d.Emit.Kind]
	if !ok {
		return cerrors.Errorf("key %s: rule names unknown emit kind %q", d.KV.Key, d.Emit.Kind)
	}
	if onlyUnset && d.Emit.Kind == KindSetting && p.Has(d.Emit.Args["side"], d.Emit.Args["name"]) {
		return nil
	}
	return k(p, d)
}

// setting writes the value as one setting. Arguments: side (source or
// destination) and name. A secret value becomes a ${VAR} reference.
func setting(p *Plan, d *rules.Decision) error {
	side, name := d.Emit.Args["side"], d.Emit.Args["name"]
	if name == "" {
		return cerrors.Errorf("key %s: emit kind setting needs a name", d.KV.Key)
	}
	value := d.KV.Value.Reveal() // the one place a value enters a pipeline
	var env string
	if d.Secret || IsSecretKey(d.KV.Key) || rules.IsReference(value) {
		value, env = p.secretRef(d)
	}
	path, err := p.Set(side, name, value, d.KV.Key)
	var conflict *ConflictError
	switch {
	case cerrors.As(err, &conflict):
		d.Entry = kcmigrate.Entry{
			Key: d.Entry.Key, Status: kcmigrate.StatusUnsupported, Origin: d.Entry.Origin, Blocking: true,
			Code:   kcmigrate.CodeKeyConflict.Reason(),
			Reason: d.KV.Key + " and " + conflict.Owner + " both map to the setting " + name + "; which one Kafka Connect used depends on the connector.",
			Fix:    "Remove one of " + d.KV.Key + " and " + conflict.Owner + " from the Kafka Connect config, then re-run.",
		}
		return nil
	case err != nil:
		return cerrors.Errorf("key %s: %w", d.KV.Key, err)
	}
	d.Entry.Target = &kcmigrate.EntryTarget{ConfigPath: path, Env: env}
	d.Entry.Secret = env != ""
	return nil
}

var envReference = regexp.MustCompile(`^\$\{env:([A-Za-z_][A-Za-z0-9_]*)\}$`)

// wholeReference matches a value that is exactly one config-provider
// reference. A reference embedded in other text is treated as a literal
// secret, because the surrounding text may be part of the secret.
var wholeReference = regexp.MustCompile(`^\$\{[^}]*?:[^}]*\}$`)

var envUnsafe = regexp.MustCompile(`[^A-Z0-9]+`)

// secretRef returns the ${VAR} that replaces d's value and records the
// variable. ${env:NAME} keeps its variable; any other value, literal or
// reference, gets KC_<CONNECTOR>_<KEY>.
func (p *Plan) secretRef(d *rules.Decision) (ref, env string) {
	raw := d.KV.Value.Reveal()
	s := Secret{Connector: p.ID, Key: d.KV.Key, Origin: d.KV.Origin.String()}
	if m := envReference.FindStringSubmatch(raw); m != nil {
		s.Env = m[1]
	} else {
		s.Env = strings.Trim(envUnsafe.ReplaceAllString("KC_"+strings.ToUpper(p.ID+"_"+d.KV.Key), "_"), "_")
		switch {
		case wholeReference.MatchString(raw):
			s.Reference = raw // a locator such as ${file:/etc/kc/db.properties:password}, not the secret
		default:
			s.Literal = true
		}
		// Unique across the run: two keys or connectors that sanitize to
		// one name keep separate variables.
		s.Env = p.envs.claim(s.Env, p.ID+"\x00"+s.Key)
	}
	if s.Reference != "" {
		d.Entry.Reason = strings.TrimSpace(d.Entry.Reason + " The value was the config-provider reference " + s.Reference + "; populate " + s.Env + " from it.")
	}
	p.secrets = append(p.secrets, s)
	return "${" + s.Env + "}", s.Env
}

// secretSuffixes and secretKeys implement the conservative secret list of
// the base design (Secrets, rule 1). A false positive only turns a value into
// a variable; a false negative leaks, so the list errs wide.
var (
	secretSuffixes = []string{"password", "secret", "token", "key", "credentials", "jaas.config", "user.info"}
	secretKeys     = map[string]bool{"aws.secret.access.key": true, "aws.access.key.id": true}
)

// IsSecretKey reports whether a key name marks its value as a secret.
func IsSecretKey(key string) bool {
	k := strings.ToLower(key)
	if secretKeys[k] {
		return true
	}
	for _, s := range secretSuffixes {
		if strings.HasSuffix(k, s) {
			return true
		}
	}
	return false
}

// CheckActions returns an error naming every action that has no registered
// kind or lacks the arguments its kind needs. Rule data is checked with it
// in tests, so a bad action fails the build instead of a run.
func CheckActions(refs []rules.ActionRef) error {
	var errs []error
	for _, r := range refs {
		a := r.Action
		if _, ok := kinds[a.Kind]; !ok {
			errs = append(errs, cerrors.Errorf("%s: key %s: unknown emit kind %q", r.File, r.Key, a.Kind))
			continue
		}
		if a.Kind == KindSetting {
			if side := a.Args["side"]; side != SideSource && side != SideDestination {
				errs = append(errs, cerrors.Errorf("%s: key %s: setting needs side source or destination", r.File, r.Key))
			}
			if a.Args["name"] == "" {
				errs = append(errs, cerrors.Errorf("%s: key %s: setting needs a name", r.File, r.Key))
			}
		}
	}
	return cerrors.Join(errs...)
}

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
	KindNone       = "none"
	KindSetting    = "setting"
	KindTLS        = "tls-from-protocol"
	KindSASLJAAS   = "sasl-jaas"
	KindHaltOnFail = "dlq-halt"
	KindList       = "list-setting"
)

var kinds = map[string]Kind{
	KindNone:       func(*Plan, *rules.Decision) error { return nil },
	KindSetting:    setting,
	KindTLS:        tlsFromProtocol,
	KindSASLJAAS:   saslJAAS,
	KindHaltOnFail: haltOnFailure,
	KindList:       listSetting,
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

// Apply applies the connector's decisions to p at LayerConnector, in order.
// Entries are refined in place; an entry whose value a higher layer replaced
// says so and has no target.
func Apply(p *Plan, ds []rules.Decision) error {
	for i := range ds {
		if err := apply(p, &ds[i], LayerConnector); err != nil {
			return err
		}
	}
	for i := range ds {
		if by, ok := p.superseded[ds[i].KV.Key]; ok && ds[i].Entry.Target != nil {
			markOverridden(&ds[i], by)
		}
	}
	return nil
}

// ApplyDefaults applies worker decisions to p at LayerWorker, after Apply:
// a worker setting is inherited only where no higher layer set it, as Kafka
// Connect merges the worker's keys under the connector's. ds is not
// modified, since one worker setting lands in many pipelines; the entries as
// applied to p are returned, index-aligned with ds, so the caller can record
// a downgrade (a worker value that cannot be emitted) on the worker's own
// entry. Dropping the refined copy would let a pipeline run without the
// setting it needed.
func ApplyDefaults(p *Plan, ds []rules.Decision) ([]kcmigrate.Entry, error) {
	out := make([]kcmigrate.Entry, len(ds))
	for i := range ds {
		d := ds[i]
		if err := apply(p, &d, LayerWorker); err != nil {
			return nil, err
		}
		out[i] = d.Entry
	}
	return out, nil
}

func apply(p *Plan, d *rules.Decision, base int) error {
	if d.Emit == nil {
		return nil
	}
	k, ok := kinds[d.Emit.Kind]
	if !ok {
		return cerrors.Errorf("key %s: rule names unknown emit kind %q", d.KV.Key, d.Emit.Kind)
	}
	offset, err := layerOffset(*d.Emit)
	if err != nil {
		return cerrors.Errorf("key %s: %w", d.KV.Key, err)
	}
	p.layer = base + offset
	if d.Emit.Args["layer"] == layerFallback {
		p.layer = LayerFallback
	}
	return k(p, d)
}

// layerFallback marks an action whose setting is used only when no worker
// or connector layer sets it (MirrorMaker's target.cluster.bootstrap.servers:
// Kafka Connect's producer writes to the worker's cluster).
const layerFallback = "fallback"

// layerOffset reads the optional layer argument: a digit 0-9, or fallback.
func layerOffset(a rules.Action) (int, error) {
	l, ok := a.Args["layer"]
	if !ok || l == layerFallback {
		return 0, nil
	}
	if len(l) != 1 || l[0] < '0' || l[0] > '9' {
		return 0, cerrors.Errorf("layer must be a digit 0-9, not %q", l)
	}
	return int(l[0] - '0'), nil
}

func markOverridden(d *rules.Decision, by string) {
	d.Entry.Target = nil
	d.Entry.Reason = strings.TrimSpace(d.Entry.Reason + " Overridden by " + by + ", which Kafka Connect applies after it.")
}

// setting writes the value as one setting. Arguments: side (source or
// destination) and name. A secret value becomes a ${VAR} reference.
func setting(p *Plan, d *rules.Decision) error {
	side, name := d.Emit.Args["side"], d.Emit.Args["name"]
	if name == "" {
		return cerrors.Errorf("key %s: emit kind setting needs a name", d.KV.Key)
	}
	var value, env string
	if v, ok := d.Emit.Args["value"]; ok {
		value = v // a mapped value from rule data, for example acks=-1 -> all
	} else {
		value = d.KV.Value.Reveal() // the one place a raw value enters a pipeline
		if d.Secret || kcmigrate.IsSecretKey(d.KV.Key) || rules.IsReference(value) {
			value, env = p.secretRef(d)
		} else {
			value = strings.TrimSpace(value) // ConfigDef.parseType trims string values
		}
	}
	return set(p, d, side, name, value, env)
}

// set writes one setting for d and records the target, turning a conflict
// into a blocking finding and a missing side into a non-blocking one.
func set(p *Plan, d *rules.Decision, side, name, value, env string) error {
	path, overriddenBy, err := p.Set(side, name, value, d.KV.Key)
	if err == nil && overriddenBy != "" {
		markOverridden(d, overriddenBy)
		return nil
	}
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
	case cerrors.Is(err, ErrNoSide):
		d.Entry = kcmigrate.Entry{
			Key: d.Entry.Key, Status: kcmigrate.StatusUnsupported, Origin: d.Entry.Origin,
			Code:   kcmigrate.CodeKeyUnsupported.Reason(),
			Reason: d.KV.Key + " configures the Kafka " + side + ", which this pipeline does not have.",
			Fix:    "Remove " + d.KV.Key + " from the Kafka Connect config; it does not apply to this connector.",
		}
		return nil
	case err != nil:
		return cerrors.Errorf("key %s: %w", d.KV.Key, err)
	}
	if d.Entry.Target != nil { // a kind that sets several settings points at the first
		return nil
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
		switch a.Kind {
		case KindSetting, KindTLS, KindSASLJAAS, KindList:
			switch a.Args["side"] {
			case SideSource, SideDestination, SideKafka, SideProducer, SideConsumer:
			default:
				errs = append(errs, cerrors.Errorf("%s: key %s: %s needs side source, destination, kafka, producer or consumer", r.File, r.Key, a.Kind))
			}
		}
		if _, err := layerOffset(a); err != nil {
			errs = append(errs, cerrors.Errorf("%s: key %s: %w", r.File, r.Key, err))
		}
		if (a.Kind == KindSetting || a.Kind == KindList) && a.Args["name"] == "" {
			errs = append(errs, cerrors.Errorf("%s: key %s: setting needs a name", r.File, r.Key))
		}
	}
	return cerrors.Join(errs...)
}

// tlsFromProtocol maps security.protocol onto tls.enabled. Argument: side.
// SSL and SASL_SSL enable TLS; PLAINTEXT and SASL_PLAINTEXT write nothing
// (TLS off is the connector default). Rule data restricts the values.
func tlsFromProtocol(p *Plan, d *rules.Decision) error {
	switch strings.ToUpper(strings.TrimSpace(d.KV.Value.Reveal())) {
	case "SSL", "SASL_SSL":
		return set(p, d, d.Emit.Args["side"], "tls.enabled", "true", "")
	default:
		// Written explicitly, so a lower layer (a worker's
		// producer.security.protocol=SSL) cannot turn TLS on under a
		// connector's PLAINTEXT override.
		return set(p, d, d.Emit.Args["side"], "tls.enabled", "false", "")
	}
}

// saslJAAS splits sasl.jaas.config into saslUsername and saslPassword (the
// password always as a ${VAR} reference). Argument: side. Anything parseJAAS
// refuses is unsupported and blocking: authenticating differently would
// fail, or succeed as someone else.
func saslJAAS(p *Plan, d *rules.Decision) error {
	login, ok := parseJAAS(d.KV.Value.Reveal()) // parsed here; only the username is written as is
	if !ok {
		d.Entry = kcmigrate.Entry{
			Key: d.Entry.Key, Status: kcmigrate.StatusUnsupported, Origin: d.Entry.Origin, Blocking: true,
			Code:   kcmigrate.CodeKeyUnsupported.Reason(),
			Reason: "sasl.jaas.config is not exactly one PlainLoginModule or ScramLoginModule, flag required, with only username and password options and no backslash escapes, so it cannot be turned into saslUsername and saslPassword without changing how the client authenticates.",
			Fix:    "Set saslMechanism, saslUsername and saslPassword on the migrated pipeline by hand, or keep this connector on Kafka Connect.",
		}
		return nil
	}
	side := d.Emit.Args["side"]
	if err := set(p, d, side, "saslUsername", login.username, ""); err != nil || d.Entry.Code != "" {
		return err
	}
	ref, env := p.secretRef(d)
	if err := set(p, d, side, "saslPassword", ref, env); err != nil {
		return err
	}
	d.Entry.Secret = true
	d.Entry.Target.Env = env
	d.Entry.Reason = strings.TrimSpace(d.Entry.Reason + " The username is written to saslUsername; saslPassword is ${" + env + "}, to be filled with the password from this JAAS config.")
	return nil
}

// haltOnFailure writes the halting dead-letter queue (errors.tolerance=none).
func haltOnFailure(p *Plan, d *rules.Decision) error {
	d.Entry.Target = &kcmigrate.EntryTarget{ConfigPath: p.HaltOnFailure()}
	return nil
}

// listSetting writes a Kafka Connect LIST value (topics) as a comma list.
// Kafka trims the value and splits on \s*,\s* (ConfigDef.parseType); an
// empty element is kept by Kafka and refused here. Arguments: side, name.
func listSetting(p *Plan, d *rules.Decision) error {
	parts := listSplit.Split(strings.TrimSpace(d.KV.Value.Reveal()), -1)
	for _, part := range parts {
		if part == "" {
			d.Entry = kcmigrate.Entry{
				Key: d.Entry.Key, Status: kcmigrate.StatusUnsupported, Origin: d.Entry.Origin, Blocking: true,
				Code:   kcmigrate.CodeKeyUnsupported.Reason(),
				Reason: d.KV.Key + " has an empty element; Kafka Connect keeps it as an empty name.",
				Fix:    "Remove the empty element (a doubled or trailing comma) from " + d.KV.Key + ".",
			}
			return nil
		}
	}
	return set(p, d, d.Emit.Args["side"], d.Emit.Args["name"], strings.Join(parts, ","), "")
}

var listSplit = regexp.MustCompile(`\s*,\s*`)

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
	"fmt"
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

// ApplyAll applies a connector's decisions and the worker's to p, layer by
// layer as Kafka Connect merges them, and returns the worker entries as
// applied to p, index-aligned with worker.
//
// The connector's entries are refined in place. worker is not modified,
// since one worker setting lands in many pipelines; the returned copies let
// the caller record a downgrade (a worker value that cannot be emitted) on
// the worker's own entry. Dropping the refined copy would let a pipeline run
// without the setting it needed.
//
// Order: the connector's keys, then the worker's (each setting inherited
// only where no higher layer set it), then the fallback cluster. Entries a
// higher layer replaced are marked only after every layer is applied, so a
// value replaced by a later layer says so too.
func ApplyAll(p *Plan, connector, worker []rules.Decision) ([]kcmigrate.Entry, error) {
	var fallback []int
	for i := range connector {
		if isFallback(connector[i].Emit) {
			fallback = append(fallback, i)
			continue
		}
		if err := apply(p, &connector[i], OriginConnector, LayerConnector); err != nil {
			return nil, err
		}
	}
	ws := make([]rules.Decision, len(worker))
	for i := range worker {
		ws[i] = worker[i]
		if err := apply(p, &ws[i], OriginWorker, LayerWorker); err != nil {
			return nil, err
		}
	}
	if err := applyFallback(p, connector, fallback); err != nil {
		return nil, err
	}
	for i := range connector {
		if by, ok := p.supersededBy(OriginConnector, connector[i].KV.Key); ok && connector[i].Entry.Target != nil {
			markOverridden(&connector[i], by)
		}
	}
	out := make([]kcmigrate.Entry, len(ws))
	for i := range ws {
		if by, ok := p.supersededBy(OriginWorker, ws[i].KV.Key); ok && ws[i].Entry.Target != nil {
			markOverridden(&ws[i], by)
		}
		out[i] = ws[i].Entry
	}
	return out, nil
}

func isFallback(a *rules.Action) bool {
	return a != nil && a.Args["layer"] == layerFallback
}

// applyFallback applies the fallback decisions (MirrorMaker's
// target.cluster.*), per side, as a whole or not at all. They describe one
// cluster: if a higher layer already names the side's brokers (the worker's
// bootstrap.servers, a producer.override.bootstrap.servers), the records go
// to that cluster, and taking the fallback's credentials for it would mix
// two clusters' settings.
func applyFallback(p *Plan, ds []rules.Decision, idx []int) error {
	used := map[string]string{} // side -> "" if the fallback is used, else the key naming the brokers
	for _, i := range idx {
		side := ds[i].Emit.Args["side"]
		if _, ok := used[side]; !ok {
			used[side] = p.brokersOwner(side)
		}
	}
	for _, i := range idx {
		d := &ds[i]
		if by := used[d.Emit.Args["side"]]; by != "" {
			d.Entry.Target = nil
			d.Entry.Reason = strings.TrimSpace(d.Entry.Reason + " Not used: " + by + " names the brokers Kafka Connect writes to, and this key's cluster applies only when nothing else does.")
			continue
		}
		if err := apply(p, d, OriginConnector, LayerFallback); err != nil {
			return err
		}
	}
	return nil
}

// brokersOwner returns the key that set servers on side, or "".
func (p *Plan) brokersOwner(side string) string {
	idx, err := p.sides(side)
	if err != nil {
		return ""
	}
	for _, i := range idx {
		if o, ok := p.owner[fmt.Sprintf("/pipelines/0/connectors/%d/settings/servers", i)]; ok {
			return o.key
		}
	}
	return ""
}

func apply(p *Plan, d *rules.Decision, origin string, base int) error {
	if d.Emit != nil {
		if err := applyKind(p, d, origin, base); err != nil {
			return err
		}
	}
	if d.Entry.Blocking {
		claimAll(p, d, origin, base)
	}
	return nil
}

// claimAll makes a blocked key the owner of every setting its rule could
// write, at the layer of each action, so that the pipeline does not run
// with a lower layer's value Kafka Connect would not have used. The setting
// stays unset; the blocking entry says why. Invariant: a value never reaches
// a pipeline from a layer below a key Kafka Connect would apply.
func claimAll(p *Plan, d *rules.Decision, origin string, base int) {
	for _, a := range d.Claims {
		offset, err := layerOffset(a)
		if err != nil || isFallback(&a) {
			continue // a fallback has no lower layer; a bad layer fails in applyKind and in tests
		}
		p.origin, p.layer = origin, base+offset
		for _, name := range claimedSettings(a) {
			p.claim(a.Args["side"], name, d.KV.Key)
		}
	}
}

// claimedSettings names the settings an action writes.
func claimedSettings(a rules.Action) []string {
	switch a.Kind {
	case KindSetting, KindList:
		return []string{a.Args["name"]}
	case KindTLS:
		return []string{"tls.enabled"}
	case KindSASLJAAS:
		return []string{"saslUsername", "saslPassword"}
	}
	return nil
}

func applyKind(p *Plan, d *rules.Decision, origin string, base int) error {
	k, ok := kinds[d.Emit.Kind]
	if !ok {
		return cerrors.Errorf("key %s: rule names unknown emit kind %q", d.KV.Key, d.Emit.Kind)
	}
	offset, err := layerOffset(*d.Emit)
	if err != nil {
		return cerrors.Errorf("key %s: %w", d.KV.Key, err)
	}
	p.origin = origin
	p.layer = base + offset
	if isFallback(d.Emit) {
		p.layer = LayerFallback
	}
	return k(p, d)
}

// layerFallback marks an action whose cluster is used only when no worker
// or connector layer names the side's brokers (MirrorMaker's
// target.cluster.*: Kafka Connect's producer writes to the worker's
// cluster).
const layerFallback = "fallback"

// layerOffsets are the named layers within a rule file's base, lowest
// first. Kafka Connect merges a connector's client config as: the
// connector's own keys, then its client-prefixed keys, then the
// cluster-specific client keys, then producer.override./consumer.override.
// (MirrorMaker's source.cluster. < consumer. < source.consumer.; the
// worker's top-level keys < its producer./consumer. keys). override is
// above every other connector layer, so no class rule can outrank it.
var layerOffsets = map[string]int{
	"":               0,
	"client":         1,
	"cluster-client": 2,
	"override":       5,
}

// layerOffset reads the optional layer argument: a name of layerOffsets, or
// fallback.
func layerOffset(a rules.Action) (int, error) {
	l := a.Args["layer"]
	if l == layerFallback {
		return 0, nil
	}
	off, ok := layerOffsets[l]
	if !ok {
		return 0, cerrors.Errorf("layer must be client, cluster-client, override or fallback, not %q", l)
	}
	return off, nil
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

// IsLiteralSecret reports whether d holds a secret as a literal value in its
// input: a secret key whose value is not exactly one config-provider
// reference. The same test secretRef applies, so the report's plaintext note
// and the variables agree.
func IsLiteralSecret(d rules.Decision) bool {
	raw := d.KV.Value.Reveal() // compared only
	if !d.Secret && !kcmigrate.IsSecretKey(d.KV.Key) || strings.TrimSpace(raw) == "" {
		return false
	}
	return envReference.FindStringSubmatch(raw) == nil && !wholeReference.MatchString(raw)
}

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
	// A username and password are one login: a higher layer that set the
	// username set the password too, and the entry is overridden as a
	// whole, without a variable.
	if err := set(p, d, side, "saslUsername", login.username, ""); err != nil || d.Entry.Code != "" || d.Entry.Target == nil {
		return err
	}
	ref, env := p.secretRef(d)
	if err := set(p, d, side, "saslPassword", ref, env); err != nil || d.Entry.Code != "" || d.Entry.Target == nil {
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

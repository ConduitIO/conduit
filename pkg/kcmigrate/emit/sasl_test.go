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
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/matryer/is"
)

func saslTestPlan(t *testing.T) *Plan {
	t.Helper()
	p, err := NewPlan("c", rules.ScopeSource, []rules.Emit{
		{Plugin: "builtin:kafka", Role: SideSource},
		{Plugin: "builtin:kafka", Role: SideDestination},
	}, NewEnvNames())
	if err != nil {
		t.Fatal(err)
	}
	return p
}

// setSASL writes the three SASL settings of side, the way the sasl kinds do,
// and returns one entry per setting key.
func setSASL(t *testing.T, p *Plan, side, env string) []kcmigrate.Entry {
	t.Helper()
	var entries []kcmigrate.Entry
	for _, kv := range []struct{ name, value string }{
		{"saslMechanism", "PLAIN"}, {"saslUsername", "u"}, {"saslPassword", "${" + env + "}"},
	} {
		path, _, err := p.Set(side, kv.name, kv.value, side+"."+kv.name)
		if err != nil {
			t.Fatal(err)
		}
		e := kcmigrate.Entry{Key: side + "." + kv.name, Status: kcmigrate.StatusTranslated, Target: &kcmigrate.EntryTarget{ConfigPath: path}}
		if kv.name == "saslPassword" {
			e.Target.Env = env
			e.Secret = true
			p.secrets = append(p.secrets, Secret{Env: env, Connector: "c", Key: e.Key})
		}
		entries = append(entries, e)
	}
	return entries
}

func setProtocol(t *testing.T, p *Plan, side, protocol string) {
	t.Helper()
	tls := "false"
	if protocol == protocolSSL || protocol == protocolSASLSSL {
		tls = "true"
	}
	if _, _, err := p.Set(side, "tls.enabled", tls, side+".security.protocol"); err != nil {
		t.Fatal(err)
	}
	p.protocol[keyRef{origin: p.origin, key: side + ".security.protocol"}] = protocol
}

func TestDropUnprotectedSASL(t *testing.T) {
	for _, protocol := range []string{"", "PLAINTEXT", "SSL", "SASL_PLAINTEXT"} {
		t.Run("protocol "+protocol, func(t *testing.T) {
			is := is.New(t)
			p := saslTestPlan(t)
			if protocol != "" {
				setProtocol(t, p, SideSource, protocol)
			}
			entries := setSASL(t, p, SideSource, "KC_SECRET")

			DropUnprotectedSASL(p, entries)

			src := p.connector(SideSource).Settings
			for _, name := range saslSettings {
				_, ok := src[name]
				is.True(!ok) // never emitted with TLS off or without SASL_SSL
			}
			is.Equal(len(p.secrets), 0) // no variable for a dropped secret
			for _, e := range entries {
				is.Equal(e.Status, kcmigrate.StatusUnsupported)
				is.True(e.Blocking)
				is.True(e.Target == nil)
				is.True(!e.Secret)
				is.True(strings.Contains(e.Reason, "Dropped"))
			}
		})
	}

	t.Run("SASL_SSL keeps everything", func(t *testing.T) {
		is := is.New(t)
		p := saslTestPlan(t)
		setProtocol(t, p, SideSource, protocolSASLSSL)
		entries := setSASL(t, p, SideSource, "KC_SECRET")

		DropUnprotectedSASL(p, entries)

		is.Equal(p.connector(SideSource).Settings["saslMechanism"], "PLAIN")
		is.Equal(len(p.secrets), 1)
		for _, e := range entries {
			is.Equal(e.Status, kcmigrate.StatusTranslated)
			is.True(e.Target != nil)
		}
	})

	t.Run("sides are decided on their own", func(t *testing.T) {
		is := is.New(t)
		p := saslTestPlan(t)
		setProtocol(t, p, SideSource, protocolSASLSSL)
		setProtocol(t, p, SideDestination, "PLAINTEXT")
		src := setSASL(t, p, SideSource, "KC_SRC")
		dst := setSASL(t, p, SideDestination, "KC_DST")

		DropUnprotectedSASL(p, src, dst)

		is.Equal(p.connector(SideSource).Settings["saslUsername"], "u")
		_, ok := p.connector(SideDestination).Settings["saslUsername"]
		is.True(!ok)
		is.Equal(len(p.secrets), 1)
		is.Equal(p.secrets[0].Env, "KC_SRC")
		is.Equal(src[2].Status, kcmigrate.StatusTranslated)
		is.Equal(dst[2].Status, kcmigrate.StatusUnsupported)
	})

	t.Run("a variable shared by both sides stays for the side that keeps it", func(t *testing.T) {
		is := is.New(t)
		p := saslTestPlan(t)
		setProtocol(t, p, SideSource, protocolSASLSSL)
		setProtocol(t, p, SideDestination, "PLAINTEXT")
		src := setSASL(t, p, SideSource, "KC_SHARED")
		dst := setSASL(t, p, SideDestination, "KC_SHARED")

		DropUnprotectedSASL(p, src, dst)

		is.Equal(p.connector(SideSource).Settings["saslPassword"], "${KC_SHARED}")
		is.True(len(p.secrets) >= 1)
		found := false
		for _, s := range p.secrets {
			found = found || s.Env == "KC_SHARED"
		}
		is.True(found) // the kept side still references it
		is.Equal(src[2].Status, kcmigrate.StatusTranslated)
		is.True(src[2].Target != nil && src[2].Target.Env == "KC_SHARED")
		is.Equal(dst[2].Status, kcmigrate.StatusUnsupported)
	})
}

// A rule that routes SASL_PLAINTEXT to the emitter fails closed: the shipped
// rule data blocks it before it gets here.
func TestTLSFromProtocol_SASLPlaintextFailsClosed(t *testing.T) {
	is := is.New(t)
	p := saslTestPlan(t)
	d := &rules.Decision{
		KV:    kcmigrate.KV{Key: "security.protocol", Value: kcmigrate.NewValue("SASL_PLAINTEXT")},
		Emit:  &rules.Action{Kind: KindTLS, Args: map[string]string{"side": SideSource}},
		Entry: kcmigrate.Entry{Key: "security.protocol"},
	}
	err := tlsFromProtocol(p, d)
	is.True(err != nil)
	is.True(strings.Contains(err.Error(), "SASL_PLAINTEXT"))
	_, ok := p.connector(SideSource).Settings["tls.enabled"]
	is.True(!ok) // nothing was written
}

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
	"os"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/kcmigrate"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/matryer/is"
)

// TestActions_RuleData checks every emit action of the embedded rule data
// and of the test rules against the registered kinds.
func TestActions_RuleData(t *testing.T) {
	is := is.New(t)
	builtin, err := rules.Builtin()
	is.NoErr(err)
	is.NoErr(CheckActions(builtin.Actions()))
	test, err := rules.Load(os.DirFS("../rules/testdata/valid"))
	is.NoErr(err)
	is.True(len(test.Actions()) > 0)
	is.NoErr(CheckActions(test.Actions()))
}

func TestCheckActions_Rejects(t *testing.T) {
	is := is.New(t)
	err := CheckActions([]rules.ActionRef{
		{File: "f", Key: "a", Action: rules.Action{Kind: "nope"}},
		{File: "f", Key: "b", Action: rules.Action{Kind: "setting", Args: map[string]string{"name": "x"}}},
		{File: "f", Key: "c", Action: rules.Action{Kind: "setting", Args: map[string]string{"side": "source"}}},
	})
	is.True(err != nil)
	for _, want := range []string{"unknown emit kind", "needs side", "needs a name"} {
		is.True(strings.Contains(err.Error(), want))
	}
}

func TestIsSecretKey(t *testing.T) {
	is := is.New(t)
	secret := []string{
		"database.password", "connection.password", "aws.secret.access.key", "sasl.jaas.config",
		"basic.auth.user.info", "s3.sse.customer.key", "auth.token", "x.credentials", "Some.PASSWORD",
	}
	for _, k := range secret {
		is.True(kcmigrate.IsSecretKey(k))
	}
	for _, k := range []string{"key.converter", "message.key.columns", "database.hostname", "tasks.max"} {
		is.True(!kcmigrate.IsSecretKey(k))
	}
}

func TestPlan(t *testing.T) {
	is := is.New(t)
	_, err := NewPlan("p", []rules.Emit{{Plugin: "builtin:a", Role: "source"}}, NewEnvNames())
	is.True(err != nil) // no destination

	p, err := NewPlan("my connector/1", []rules.Emit{{Plugin: "builtin:a", Role: "source"}, {Plugin: "builtin:b", Role: "destination"}}, NewEnvNames())
	is.NoErr(err)
	is.Equal(p.File, "pipelines/my_connector_1.yaml")
	p.layer = LayerConnector
	path, _, err := p.Set(SideDestination, "a/b", "1", "k1")
	is.NoErr(err)
	is.Equal(path, "/pipelines/0/connectors/1/settings/a~1b")
	_, _, err = p.Set(SideDestination, "a/b", "1", "k1") // the same key may set it again
	is.NoErr(err)
	_, _, err = p.Set(SideDestination, "a/b", "2", "k2")
	var conflict *ConflictError
	is.True(err != nil && strings.Contains(err.Error(), "already set by k1"))
	is.True(asConflict(err, &conflict))
	_, _, err = p.Set("middle", "x", "1", "k")
	is.True(err != nil)

	for _, k := range []string{"z", "m", "a"} {
		_, _, err := p.Set(SideSource, k, k, k)
		is.NoErr(err)
	}
	first, err := p.YAML("running", "d")
	is.NoErr(err)
	for range 10 {
		again, err := p.YAML("running", "d")
		is.NoErr(err)
		is.Equal(first, again)
	}
	is.True(strings.Index(string(first), "a: a") < strings.Index(string(first), "z: z")) // sorted settings
}

func asConflict(err error, target **ConflictError) bool {
	return cerrors.As(err, target)
}

// A higher layer replaces a lower one in either order, and the replaced key
// is recorded; the same layer is a conflict.
func TestPlan_Layers(t *testing.T) {
	is := is.New(t)
	p, err := NewPlan("p", []rules.Emit{{Plugin: "builtin:kafka", Role: "source"}, {Plugin: "builtin:kafka", Role: "destination"}}, NewEnvNames())
	is.NoErr(err)

	p.layer = LayerWorker
	_, by, err := p.Set(SideSource, "servers", "w:9092", "bootstrap.servers")
	is.NoErr(err)
	is.Equal(by, "")
	p.layer = LayerConnector
	_, by, err = p.Set(SideSource, "servers", "c:9092", "source.cluster.bootstrap.servers")
	is.NoErr(err)
	is.Equal(by, "")
	is.Equal(p.Superseded()["bootstrap.servers"], "source.cluster.bootstrap.servers")

	p.layer = LayerConnector + 2
	_, _, err = p.Set(SideSource, "saslMechanism", "SCRAM-SHA-512", "source.consumer.sasl.mechanism")
	is.NoErr(err)
	p.layer = LayerConnector
	_, by, err = p.Set(SideSource, "saslMechanism", "PLAIN", "source.cluster.sasl.mechanism")
	is.NoErr(err)
	is.Equal(by, "source.consumer.sasl.mechanism") // lower layer arriving later is skipped

	p.layer = LayerConnector
	_, _, err = p.Set(SideSource, "servers", "d:9092", "other.key")
	is.True(err != nil) // same layer, different key

	y, err := p.YAML("running", "")
	is.NoErr(err)
	is.True(strings.Contains(string(y), "servers: c:9092"))
	is.True(strings.Contains(string(y), "saslMechanism: SCRAM-SHA-512"))
}

func TestFinish_MM2Topic(t *testing.T) {
	cases := []struct {
		name string
		cfg  map[string]string
		want string
	}{
		{"defaults", nil, `source.{{ index .Metadata "opencdc.collection" }}`},
		{"alias and separator", map[string]string{"source.cluster.alias": "us-east", "replication.policy.separator": "_"}, `us-east_{{ index .Metadata "opencdc.collection" }}`},
		{"identity", map[string]string{"replication.policy.class": "org.apache.kafka.connect.mirror.IdentityReplicationPolicy", "source.cluster.alias": "x"}, `{{ index .Metadata "opencdc.collection" }}`},
		{"custom policy", map[string]string{"replication.policy.class": "com.example.Policy"}, ""},
	}
	for _, tt := range cases {
		t.Run(tt.name, func(t *testing.T) {
			is := is.New(t)
			p, err := NewPlan("mm", []rules.Emit{{Plugin: "builtin:kafka", Role: "source"}, {Plugin: "builtin:kafka", Role: "destination"}}, NewEnvNames())
			is.NoErr(err)
			p.config = tt.cfg
			notes, err := Finish(p, []rules.Action{{Kind: FinishMM2Topic}})
			is.NoErr(err)
			is.Equal(len(notes), 0)
			is.Equal(p.connector(SideDestination).Settings["topic"], tt.want)
		})
	}
	is := is.New(t)
	p, _ := NewPlan("mm", []rules.Emit{{Plugin: "builtin:kafka", Role: "source"}, {Plugin: "builtin:kafka", Role: "destination"}}, NewEnvNames())
	p.config = map[string]string{"source.cluster.alias": "a{{b"}
	notes, err := Finish(p, []rules.Action{{Kind: FinishMM2Topic}})
	is.NoErr(err)
	is.True(len(notes) == 1 && notes[0].Blocking)
	_, err = Finish(p, []rules.Action{{Kind: "nope"}})
	is.True(err != nil)
}

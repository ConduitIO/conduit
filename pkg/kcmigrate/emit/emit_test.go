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
		is.True(IsSecretKey(k))
	}
	for _, k := range []string{"key.converter", "message.key.columns", "database.hostname", "tasks.max"} {
		is.True(!IsSecretKey(k))
	}
}

func TestPlan(t *testing.T) {
	is := is.New(t)
	_, err := NewPlan("p", []rules.Emit{{Plugin: "builtin:a", Role: "source"}}, NewEnvNames())
	is.True(err != nil) // no destination

	p, err := NewPlan("my connector/1", []rules.Emit{{Plugin: "builtin:a", Role: "source"}, {Plugin: "builtin:b", Role: "destination"}}, NewEnvNames())
	is.NoErr(err)
	is.Equal(p.File, "pipelines/my_connector_1.yaml")
	path, err := p.Set(SideDestination, "a/b", "1", "k1")
	is.NoErr(err)
	is.Equal(path, "/pipelines/0/connectors/1/settings/a~1b")
	_, err = p.Set(SideDestination, "a/b", "1", "k1") // the same key may set it again
	is.NoErr(err)
	_, err = p.Set(SideDestination, "a/b", "2", "k2")
	var conflict *ConflictError
	is.True(err != nil && strings.Contains(err.Error(), "already set by k1"))
	is.True(asConflict(err, &conflict))
	_, err = p.Set("middle", "x", "1", "k")
	is.True(err != nil)

	for _, k := range []string{"z", "m", "a"} {
		_, err := p.Set(SideSource, k, k, k)
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

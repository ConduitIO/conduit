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

package drift

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
	"github.com/matryer/is"
)

// entry renders one key the way Kafka's ConfigDef.toHtml does.
func entry(key, def string) string {
	return `<li>
<h4><a id="` + key + `"></a><a id="x_` + key + `" href="#x_` + key + `">` + key + `</a></h4>
<p><p>Doc with a list:</p><ol><li>one</li><li>two</li></ol></p>
<table><tbody>
<tr><th>Type:</th><td>string</td></tr>
<tr><th>Default:</th><td>` + def + `</td></tr>
<tr><th>Importance:</th><td>high</td></tr>
</tbody></table>
</li>
`
}

func page(entries ...string) []byte {
	return []byte(`<ul class="config-list">` + "\n" + strings.Join(entries, "") + "</ul>\n")
}

func TestParseKafkaPage(t *testing.T) {
	is := is.New(t)
	keys, err := ParseKafkaPage(page(
		entry("required", ""), entry("nullable", "null"), entry("empty", `""`),
		entry("timeout.ms", "60000 (1 minute)"), entry("topics.exclude", "mm2.*\\.internal,.*\\.replica,__.*"),
		entry("amp", "a&amp;b"),
	))
	is.NoErr(err)
	is.Equal(len(keys), 6)
	is.Equal(keys["required"], Required) // an empty cell: no default at all
	is.Equal(keys["nullable"], NoDefault)
	is.Equal(keys["empty"], "")
	is.Equal(keys["timeout.ms"], "60000")
	is.Equal(keys["amp"], "a&b")
	is.Equal(keys["topics.exclude"], normalize(`mm2.*\.internal, .*\.replica, __.*`)) // list spacing does not matter

	_, err = ParseKafkaPage([]byte("<html>moved</html>"))
	is.True(err != nil) // a format change fails, it does not read as "every key removed"
	_, err = ParseKafkaPage([]byte(`<h4><a id="k"></a>k</h4><p>no table</p>`))
	is.True(err != nil)
}

func snapshot(t *testing.T, y string) rules.Snapshot {
	t.Helper()
	s, err := rules.ParseSnapshot([]byte(y))
	if err != nil {
		t.Fatal(err)
	}
	return s
}

const snapHead = "scope: source\nclass: C\nupstream: {name: kafka, version: \"4.2\"}\nsources: [x]\n"

func TestCompare(t *testing.T) {
	is := is.New(t)
	s := snapshot(t, snapHead+`check:
  site: kafka
  pages: [p]
  ignore:
    - {pattern: '^sasl\.', reason: client keys}
keys:
  - {key: same, default: "1"}
  - {key: changed, default: "1"}
  - {key: gone}
  - {key: effective, default: Simple, documented: "null"}
  - {key: prefixed.servers, undocumented: true}
  - {key: must, required: true}
  - {key: optional}
`)
	ig, err := Ignorer(s, nil)
	is.NoErr(err)
	got := Compare(s, Keys{"same": "1", "changed": "2", "effective": NoDefault, "must": Required, "optional": NoDefault, "new": NoDefault, "sasl.mechanism": NoDefault}, ig)
	is.Equal(len(got), 3)
	is.Equal(got[0], Change{Key: "changed", Kind: Default, Snapshot: "1", Page: "2"})
	is.Equal(got[1], Change{Key: "gone", Kind: Removed})
	is.Equal(got[2], Change{Key: "new", Kind: Added, Page: NoDefault})

	// An undocumented key that appears on the page is reported, so the mark
	// does not outlive the reason for it. An optional key that becomes
	// required upstream, and the reverse, are drift.
	got = Compare(s, Keys{"same": "1", "changed": "1", "gone": NoDefault, "effective": NoDefault, "prefixed.servers": NoDefault, "must": NoDefault, "optional": Required}, ig)
	is.Equal(got, []Change{
		{Key: "must", Kind: Default, Snapshot: Required, Page: NoDefault},
		{Key: "optional", Kind: Default, Snapshot: NoDefault, Page: Required},
		{Key: "prefixed.servers", Kind: Added, Page: NoDefault},
	})
	_, err = rules.ParseSnapshot([]byte(snapHead + "keys:\n  - {key: k, required: true, default: x}\n"))
	is.True(err != nil) // required and a default contradict

	// An ignore entry can name another snapshot, whose keys it leaves out.
	m := snapshot(t, snapHead+"check:\n  site: kafka\n  pages: [p]\n  ignore:\n    - {snapshot: shared, reason: shared keys}\nkeys:\n  - {key: own}\n")
	ig, err = Ignorer(m, map[string]rules.Snapshot{"shared": snapshot(t, snapHead+"keys:\n  - {key: name}\n")})
	is.NoErr(err)
	is.Equal(len(Compare(m, Keys{"own": NoDefault, "name": Required}, ig)), 0)
	_, err = Ignorer(m, nil)
	is.True(err != nil) // a dangling snapshot name
}

// pages serves Kafka pages by version, 404 for any other.
type pages map[string][]byte

func (p pages) fetch(_ context.Context, url string) ([]byte, error) {
	if b, ok := p[url]; ok {
		return b, nil
	}
	return nil, ErrNotPublished
}

func TestLatest(t *testing.T) {
	is := is.New(t)
	p := pages{}
	for _, v := range []Version{{4, 2}, {4, 3}, {4, 4}, {5, 0}, {5, 1}} {
		p[KafkaPageURL(v, "c")] = page(entry("k", ""))
	}
	got, err := Latest(context.Background(), p.fetch, Version{4, 2}, "c")
	is.NoErr(err)
	is.Equal(got, Version{5, 1}) // minors, then the next major's .0, then its minors
	got, err = Latest(context.Background(), p.fetch, Version{5, 1}, "c")
	is.NoErr(err)
	is.Equal(got, Version{5, 1})

	failing := func(context.Context, string) ([]byte, error) { return nil, cerrors.New("boom") }
	_, err = Latest(context.Background(), failing, Version{4, 2}, "c")
	is.True(err != nil) // an unreadable page is an error, not "no newer version"
}

func TestCheck(t *testing.T) {
	is := is.New(t)
	s := snapshot(t, snapHead+"check:\n  site: kafka\n  pages: [a, b]\nkeys:\n  - {key: x, default: \"1\"}\n  - {key: y, required: true}\n")
	p := pages{
		KafkaPageURL(Version{4, 2}, "a"): page(entry("x", "1")),
		KafkaPageURL(Version{4, 2}, "b"): page(entry("y", "")),
	}
	r, err := Check(context.Background(), p.fetch, "s", s, nil, Version{4, 2})
	is.NoErr(err)
	is.True(!r.Failed())
	is.Equal(r.Latest, Version{4, 2})
	is.True(strings.Contains(Markdown([]Result{r}), "matches 4.2, the newest documented version"))

	p[KafkaPageURL(Version{4, 3}, "a")] = page(entry("x", "2"), entry("z", ""))
	p[KafkaPageURL(Version{4, 3}, "b")] = page(entry("y", ""))
	r, err = Check(context.Background(), p.fetch, "s", s, nil, Version{4, 3})
	is.NoErr(err)
	is.True(r.Failed())
	is.Equal(len(r.Wrong), 0)
	is.Equal(r.Drifted, []Change{{Key: "x", Kind: Default, Snapshot: "1", Page: "2"}, {Key: "z", Kind: Added, Page: Required}})
	is.True(r.Unchecked == nil)
	md := Markdown([]Result{r})
	is.True(strings.Contains(md, "upstream 4.3: x: default 2, snapshot 1"))
	is.True(strings.Contains(md, "upstream 4.3: z: added"))

	// A release newer than the newest documented version cannot be
	// checked: it is reported, never read as "no newer version".
	r, err = Check(context.Background(), p.fetch, "s", s, nil, Version{4, 4})
	is.NoErr(err)
	is.Equal(*r.Unchecked, Version{4, 4})
	is.True(strings.Contains(Markdown([]Result{r}), "Kafka 4.4 is released, but its generated config pages are not published"))

	delete(p, KafkaPageURL(Version{4, 2}, "b"))
	_, err = Check(context.Background(), p.fetch, "s", s, nil, Version{4, 3})
	is.True(err != nil) // a missing pinned page is an error
}

func TestHTTPFetcher(t *testing.T) {
	is := is.New(t)
	retryWait = time.Millisecond
	var flaky atomic.Int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/ok":
			_, _ = w.Write([]byte("body"))
		case "/flaky":
			if flaky.Add(1) < 3 {
				w.WriteHeader(http.StatusBadGateway)
				return
			}
			_, _ = w.Write([]byte("late"))
		case "/forbidden":
			w.WriteHeader(http.StatusForbidden)
		default:
			http.NotFound(w, r)
		}
	}))
	defer srv.Close()
	f := HTTPFetcher(srv.Client())
	ctx := context.Background()
	b, err := f(ctx, srv.URL+"/ok")
	is.NoErr(err)
	is.Equal(string(b), "body")
	b, err = f(ctx, srv.URL+"/flaky")
	is.NoErr(err) // a 5xx is retried
	is.Equal(string(b), "late")
	_, err = f(ctx, srv.URL+"/missing")
	is.True(cerrors.Is(err, ErrNotPublished))
	_, err = f(ctx, srv.URL+"/forbidden")
	is.True(err != nil && !cerrors.Is(err, ErrNotPublished))
}

func TestVersion(t *testing.T) {
	is := is.New(t)
	v, err := ParseVersion("4.2")
	is.NoErr(err)
	is.Equal(KafkaPageURL(v, "connect_config"), "https://kafka.apache.org/42/generated/connect_config.html")
	for _, bad := range []string{"4", "4.10", "x.2", "4.2.1"} {
		_, err := ParseVersion(bad)
		is.True(err != nil)
	}
}

// Every committed snapshot parses, and every one with a check section has
// a valid one: the nightly job must not be the first to find out.
func TestCommittedSnapshotsHaveValidChecks(t *testing.T) {
	is := is.New(t)
	paths, err := filepath.Glob("../upstream/*.keys.yaml")
	is.NoErr(err)
	is.True(len(paths) > 0)
	snaps := map[string]rules.Snapshot{}
	for _, p := range paths {
		b, err := os.ReadFile(p)
		is.NoErr(err)
		s, err := rules.ParseSnapshot(b)
		is.NoErr(err)
		snaps[strings.TrimSuffix(filepath.Base(p), ".keys.yaml")] = s
	}
	checked := 0
	for _, s := range snaps {
		if s.Check == nil {
			continue
		}
		checked++
		_, err := Ignorer(s, snaps)
		is.NoErr(err)
		_, err = ParseVersion(s.Upstream.Version) // major.minor, as the page URL needs
		is.NoErr(err)
	}
	is.True(checked > 0)
}

func TestNewestRelease(t *testing.T) {
	is := is.New(t)
	list := pages{KafkaReleases: []byte(`<a href="4.1.2/">4.1.2/</a> <a href="4.10.0/">x</a> <a href="4.3.1/">x</a> <a href="KEYS">KEYS</a>`)}
	v, err := NewestRelease(context.Background(), list.fetch)
	is.NoErr(err)
	is.Equal(v, Version{4, 10}) // numeric, not lexical
	_, err = NewestRelease(context.Background(), pages{KafkaReleases: []byte("moved")}.fetch)
	is.True(err != nil) // an unreadable list fails, it does not read as "nothing released"
	_, err = NewestRelease(context.Background(), pages{}.fetch)
	is.True(err != nil)
}

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

// Package drift compares the committed upstream key snapshots
// (pkg/kcmigrate/rules/upstream) with the live upstream documentation, for
// the scheduled upstream-drift job (cmd/conduit/internal/kcdrift; mapping
// design 20261014-migrate-kafka-connect-mapping-and-harness.md, "Upstream
// drift").
//
// Two comparisons per snapshot:
//   - Pinned: the pages of the snapshot's own upstream version. A difference
//     means the snapshot was transcribed wrong, and the rules were written
//     against a key list upstream never had.
//   - Latest: the pages of the newest upstream version that publishes them.
//     A difference is drift: a key upstream added (a real config reports it
//     as kc.key_unknown, blocking), removed, or whose default changed (an
//     absent key now means something else, which only the defaults entries
//     catch).
//
// Either fails the job. Nothing here runs on a pull request: the pages are
// on the network. The parser and the comparison are tested offline.
package drift

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/conduitio/conduit/pkg/kcmigrate/rules"
)

// ErrNotPublished is returned by a Fetcher for a page that does not exist
// (HTTP 404): the version has no docs yet.
var ErrNotPublished = cerrors.New("page not published")

// Fetcher returns the body of url, ErrNotPublished for a missing page, or
// another error when the page could not be read.
type Fetcher func(ctx context.Context, url string) ([]byte, error)

// retryWait is the backoff step between attempts; tests shorten it.
var retryWait = 5 * time.Second

// maxPage bounds a page body. The largest Kafka config page is about 100KB.
const maxPage = 4 << 20

// HTTPFetcher fetches pages over HTTP with a timeout and a few retries for
// transient failures. A 404 is ErrNotPublished, not retried.
func HTTPFetcher(client *http.Client) Fetcher {
	return func(ctx context.Context, url string) ([]byte, error) {
		var last error
		for attempt := range 4 {
			if attempt > 0 {
				select {
				case <-ctx.Done():
					return nil, ctx.Err()
				case <-time.After(time.Duration(attempt) * retryWait):
				}
			}
			b, retry, err := fetchOnce(ctx, client, url)
			if err == nil || !retry {
				return b, err
			}
			last = err
		}
		return nil, last
	}
}

func fetchOnce(ctx context.Context, client *http.Client, url string) (body []byte, retry bool, err error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, false, cerrors.Errorf("request %s: %w", url, err)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, true, cerrors.Errorf("get %s: %w", url, err)
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		return nil, false, ErrNotPublished
	case resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests:
		return nil, true, cerrors.Errorf("get %s: %s", url, resp.Status)
	case resp.StatusCode != http.StatusOK:
		return nil, false, cerrors.Errorf("get %s: %s", url, resp.Status)
	}
	b, err := io.ReadAll(io.LimitReader(resp.Body, maxPage+1))
	if err != nil {
		return nil, true, cerrors.Errorf("read %s: %w", url, err)
	}
	if len(b) > maxPage {
		return nil, false, cerrors.Errorf("read %s: page larger than %d bytes", url, maxPage)
	}
	return b, false, nil
}

// Keys maps a key to its documented default, normalized (see cellValue).
type Keys map[string]string

var (
	// One heading per key in Kafka's generated pages (ConfigDef.toHtml):
	// <h4><a id="KEY"></a>...</h4>, then a description that may hold its
	// own lists, then <th>Default:</th><td>VALUE</td>. A key's section runs
	// to the next heading.
	kafkaHeading = regexp.MustCompile(`<h4><a id="([^"]+)"></a>`)
	kafkaDefault = regexp.MustCompile(`(?s)<th>Default:</th><td>(.*?)</td>`)
)

// ParseKafkaPage reads the keys and defaults of an Apache Kafka generated
// config page. A page with no keys is an error, so a format change fails
// the job instead of reading as "every key removed".
func ParseKafkaPage(b []byte) (Keys, error) {
	out := Keys{}
	heads := kafkaHeading.FindAllSubmatchIndex(b, -1)
	for i, h := range heads {
		end := len(b)
		if i+1 < len(heads) {
			end = heads[i+1][0]
		}
		key := html.UnescapeString(string(b[h[2]:h[3]]))
		d := kafkaDefault.FindSubmatch(b[h[1]:end])
		if d == nil {
			return nil, cerrors.Errorf("key %s has no Default row; the page format changed", key)
		}
		out[key] = cellValue(html.UnescapeString(string(d[1])))
	}
	if len(out) == 0 {
		return nil, cerrors.New("no keys found; the page format changed")
	}
	return out, nil
}

// cellValue turns a page's Default cell into a comparable value. Kafka's
// ConfigDef.toHtml writes an empty cell for a key with no default at all,
// which makes it required (NO_DEFAULT_VALUE), "null" for a key whose default
// is null (optional, unset), and `""` for the empty string. The three stay
// apart, so a key that becomes required upstream shows as drift.
func cellValue(cell string) string {
	switch cell = strings.TrimSpace(cell); cell {
	case "":
		return Required
	case "null":
		return NoDefault
	case `""`:
		return ""
	}
	return normalize(cell)
}

// snapshotValue is the value a snapshot key should show on the page.
func snapshotValue(k rules.SnapshotKey) string {
	switch {
	case k.Documented != nil:
		return cellValue(*k.Documented) // spelled as on the page
	case k.Required:
		return Required
	case k.Default == nil:
		return NoDefault
	}
	return normalize(*k.Default)
}

// Values a Change shows for a key with no default.
const (
	NoDefault = "<none>"     // optional, unset (the page's "null")
	Required  = "<required>" // no default at all (the page's empty cell)
)

var humanSuffix = regexp.MustCompile(`^(\S+) \([^)]*\)$`)

// normalize makes two spellings of one default compare equal: the page's
// "60000 (1 minute)" is 60000, and a list is compared element by element
// (the page joins with ",", hand transcriptions with ", ").
func normalize(v string) string {
	s := strings.TrimSpace(v)
	if m := humanSuffix.FindStringSubmatch(s); m != nil {
		s = m[1]
	}
	parts := strings.Split(s, ",")
	for i := range parts {
		parts[i] = strings.TrimSpace(parts[i])
	}
	return strings.Join(parts, ",")
}

// Change is one difference between a snapshot and a page.
type Change struct {
	Key      string
	Kind     string // added, removed or default
	Snapshot string // the snapshot's default (normalized), for default
	Page     string // the page's default (normalized), for default
}

// Change kinds.
const (
	Added   = "added"   // on the page, not in the snapshot
	Removed = "removed" // in the snapshot, not on the page
	Default = "default" // in both, with different defaults
)

func (c Change) String() string {
	switch c.Kind {
	case Default:
		return fmt.Sprintf("%s: default %s, snapshot %s", c.Key, c.Page, c.Snapshot)
	default:
		return c.Key + ": " + c.Kind
	}
}

// Compare returns the differences between snap and page, sorted by key.
// Page keys ignore reports true for are left out; a snapshot key marked
// undocumented must not be on the page, and one with a Documented default
// is compared with that.
func Compare(snap rules.Snapshot, page Keys, ignore func(string) bool) []Change {
	var out []Change
	seen := map[string]bool{}
	for _, k := range snap.Keys {
		seen[k.Key] = true
		pd, on := page[k.Key]
		switch {
		case k.Undocumented && on:
			out = append(out, Change{Key: k.Key, Kind: Added, Page: pd}) // now documented: drop the mark
		case k.Undocumented:
		case !on:
			out = append(out, Change{Key: k.Key, Kind: Removed})
		default:
			if want := snapshotValue(k); want != pd {
				out = append(out, Change{Key: k.Key, Kind: Default, Snapshot: want, Page: pd})
			}
		}
	}
	for k, d := range page {
		if !seen[k] && !ignore(k) {
			out = append(out, Change{Key: k, Kind: Added, Page: d})
		}
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Key < out[j].Key })
	return out
}

// Ignorer returns the ignore function of snap's check. others are the other
// snapshots by file name without .keys.yaml, for ignore entries that name
// one.
func Ignorer(snap rules.Snapshot, others map[string]rules.Snapshot) (func(string) bool, error) {
	var res []*regexp.Regexp
	keys := map[string]bool{}
	for _, ig := range snap.Check.Ignore {
		if ig.Pattern != "" {
			res = append(res, regexp.MustCompile(ig.Pattern)) // ParseSnapshot compiled it
			continue
		}
		o, ok := others[ig.Snapshot]
		if !ok {
			return nil, cerrors.Errorf("ignore names snapshot %q, which does not exist", ig.Snapshot)
		}
		for _, k := range o.Keys {
			keys[k.Key] = true
		}
	}
	return func(k string) bool {
		if keys[k] {
			return true
		}
		for _, re := range res {
			if re.MatchString(k) {
				return true
			}
		}
		return false
	}, nil
}

// Version is a major.minor upstream version.
type Version struct{ Major, Minor int }

// ParseVersion reads "4.2".
func ParseVersion(s string) (Version, error) {
	majS, minS, ok := strings.Cut(s, ".")
	a, err1 := strconv.Atoi(majS)
	b, err2 := strconv.Atoi(minS)
	if !ok || err1 != nil || err2 != nil || a < 0 || b < 0 || b > 9 {
		return Version{}, cerrors.Errorf("version %q is not major.minor with a one-digit minor", s)
	}
	return Version{a, b}, nil
}

func (v Version) String() string { return fmt.Sprintf("%d.%d", v.Major, v.Minor) }

// KafkaPageURL is the URL of a generated page of Kafka version v.
func KafkaPageURL(v Version, page string) string {
	return fmt.Sprintf("https://kafka.apache.org/%d%d/generated/%s.html", v.Major, v.Minor, page)
}

// FetchKeys fetches and merges the pages of v.
func FetchKeys(ctx context.Context, fetch Fetcher, v Version, pages []string) (Keys, error) {
	out := Keys{}
	for _, p := range pages {
		b, err := fetch(ctx, KafkaPageURL(v, p))
		if err != nil {
			return nil, err
		}
		keys, err := ParseKafkaPage(b)
		if err != nil {
			return nil, cerrors.Errorf("%s: %w", KafkaPageURL(v, p), err)
		}
		for k, d := range keys {
			out[k] = d
		}
	}
	return out, nil
}

// maxProbe bounds the versions probed past the pinned one.
const maxProbe = 12

// Latest returns the newest version after pinned whose first page is
// published, or pinned when none is. It walks minors, then the next major's
// .0, so 4.9 is followed by 5.0.
func Latest(ctx context.Context, fetch Fetcher, pinned Version, page string) (Version, error) {
	latest, v := pinned, pinned
	for range maxProbe {
		next := Version{v.Major, v.Minor + 1}
		ok, err := published(ctx, fetch, next, page)
		if err != nil {
			return Version{}, err
		}
		if !ok {
			next = Version{v.Major + 1, 0}
			if ok, err = published(ctx, fetch, next, page); err != nil {
				return Version{}, err
			}
		}
		if !ok {
			return latest, nil
		}
		latest, v = next, next
	}
	return latest, nil
}

func published(ctx context.Context, fetch Fetcher, v Version, page string) (bool, error) {
	_, err := fetch(ctx, KafkaPageURL(v, page))
	switch {
	case err == nil:
		return true, nil
	case cerrors.Is(err, ErrNotPublished):
		return false, nil
	}
	return false, err
}

// KafkaReleases lists the current Apache Kafka releases.
const KafkaReleases = "https://downloads.apache.org/kafka/"

var releaseDir = regexp.MustCompile(`href="(\d+)\.(\d+)\.\d+/"`)

// NewestRelease returns the newest major.minor in the Apache Kafka release
// list, so a release whose docs are not published yet is not mistaken for
// "no newer version".
func NewestRelease(ctx context.Context, fetch Fetcher) (Version, error) {
	b, err := fetch(ctx, KafkaReleases)
	if err != nil {
		return Version{}, cerrors.Errorf("release list: %w", err)
	}
	var newest Version
	found := false
	for _, m := range releaseDir.FindAllSubmatch(b, -1) {
		maj, err1 := strconv.Atoi(string(m[1]))
		mnr, err2 := strconv.Atoi(string(m[2]))
		if err1 != nil || err2 != nil {
			continue
		}
		v := Version{maj, mnr}
		if !found || v.Major > newest.Major || (v.Major == newest.Major && v.Minor > newest.Minor) {
			newest, found = v, true
		}
	}
	if !found {
		return Version{}, cerrors.Errorf("release list %s lists no versions; the page format changed", KafkaReleases)
	}
	return newest, nil
}

// After reports whether v is newer than o.
func (v Version) After(o Version) bool {
	return v.Major > o.Major || (v.Major == o.Major && v.Minor > o.Minor)
}

// Result is the outcome for one snapshot.
type Result struct {
	Name    string
	Pinned  Version
	Latest  Version
	Wrong   []Change // pinned page vs snapshot: the snapshot is wrong
	Drifted []Change // latest page vs snapshot: upstream moved
	// Unchecked is a release newer than Latest whose docs are not
	// published, so its keys could not be compared.
	Unchecked *Version
}

// Failed reports whether the snapshot needs a change or could not be
// checked against the newest release.
func (r Result) Failed() bool { return len(r.Wrong) > 0 || len(r.Drifted) > 0 || r.Unchecked != nil }

// Check compares one snapshot with its pinned and latest pages.
//
// released is the newest Kafka release (NewestRelease); a release newer
// than the newest documented version is reported as unchecked.
func Check(ctx context.Context, fetch Fetcher, name string, snap rules.Snapshot, others map[string]rules.Snapshot, released Version) (Result, error) {
	r := Result{Name: name}
	pinned, err := ParseVersion(snap.Upstream.Version)
	if err != nil {
		return r, err
	}
	r.Pinned, r.Latest = pinned, pinned
	ignore, err := Ignorer(snap, others)
	if err != nil {
		return r, err
	}
	keys, err := FetchKeys(ctx, fetch, pinned, snap.Check.Pages)
	if err != nil {
		return r, cerrors.Errorf("pinned %s: %w", pinned, err)
	}
	r.Wrong = Compare(snap, keys, ignore)
	if r.Latest, err = Latest(ctx, fetch, pinned, snap.Check.Pages[0]); err != nil {
		return r, err
	}
	if r.Latest != pinned {
		keys, err := FetchKeys(ctx, fetch, r.Latest, snap.Check.Pages)
		if err != nil {
			return r, cerrors.Errorf("latest %s: %w", r.Latest, err)
		}
		r.Drifted = Compare(snap, keys, ignore)
	}
	if released.After(r.Latest) {
		r.Unchecked = &released
	}
	return r, nil
}

// Markdown renders results for the job summary and the drift issue.
func Markdown(results []Result) string {
	var b strings.Builder
	b.WriteString("# Upstream key drift\n\n")
	for _, r := range results {
		switch {
		case !r.Failed() && r.Latest == r.Pinned:
			fmt.Fprintf(&b, "- `%s`: matches %s, the newest documented version.\n", r.Name, r.Pinned)
		case !r.Failed():
			fmt.Fprintf(&b, "- `%s`: matches %s and %s.\n", r.Name, r.Pinned, r.Latest)
		default:
			fmt.Fprintf(&b, "- `%s` (pinned %s, newest documented %s):\n", r.Name, r.Pinned, r.Latest)
			for _, c := range r.Wrong {
				fmt.Fprintf(&b, "  - snapshot differs from the pinned %s docs: %s\n", r.Pinned, c)
			}
			for _, c := range r.Drifted {
				fmt.Fprintf(&b, "  - upstream %s: %s\n", r.Latest, c)
			}
			if r.Unchecked != nil {
				fmt.Fprintf(&b, "  - Kafka %s is released, but its generated config pages are not published (%s), so its keys are not checked.\n", r.Unchecked, KafkaPageURL(*r.Unchecked, "connect_config"))
			}
		}
	}
	return b.String()
}

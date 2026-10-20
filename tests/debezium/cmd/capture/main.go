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

// Command capture reads every message a Debezium connector wrote and stores
// it as a golden file, byte for byte.
//
// Messages are read as raw bytes, never through a deserializer (design: Test
// plan, "Capture and normalize"). Keys and values that are valid UTF-8 are
// stored as JSON strings, which preserves their bytes exactly; anything else
// (Avro wire format) is stored base64-encoded. A null key or value (a
// tombstone) is stored as JSON null, distinct from an empty one.
//
// With -registry set, the command also stores every subject version and every
// schema ID the messages reference, so a test can resolve Avro wire-format
// schema IDs to schema text.
package main

import (
	"context"
	"encoding/base64"
	"encoding/binary"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
	"github.com/twmb/franz-go/pkg/kgo"
)

// Event is one Kafka message as written by Debezium.
type Event struct {
	Topic     string   `json:"topic"`
	Partition int32    `json:"partition"`
	Offset    int64    `json:"offset"`
	Key       *Bytes   `json:"key"`
	Value     *Bytes   `json:"value"`
	Headers   []Header `json:"headers,omitempty"`
}

// Header is one Kafka record header.
type Header struct {
	Key   string `json:"key"`
	Value *Bytes `json:"value"`
}

// Bytes holds raw message bytes. It marshals as {"text": "..."} when the bytes
// are valid UTF-8 and as {"base64": "..."} otherwise, so the stored form is
// lossless and still readable for JSON-converter output.
type Bytes struct {
	Text   *string `json:"text,omitempty"`
	Base64 *string `json:"base64,omitempty"`
}

func newBytes(b []byte) *Bytes {
	if b == nil {
		return nil
	}
	if utf8.Valid(b) {
		s := string(b)
		return &Bytes{Text: &s}
	}
	s := base64.StdEncoding.EncodeToString(b)
	return &Bytes{Base64: &s}
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "capture:", err)
		os.Exit(1)
	}
}

func run() error {
	brokers := flag.String("brokers", "", "Kafka bootstrap servers, comma separated")
	registry := flag.String("registry", "", "Schema Registry URL; empty skips schema capture")
	topicRegex := flag.String("topics", `^dbz\.`, "regular expression selecting the topics to capture")
	sentinel := flag.String("sentinel", "dbz.golden.sentinel", "topic whose first message ends the capture")
	out := flag.String("out", "", "output directory")
	timeout := flag.Duration("timeout", 3*time.Minute, "give up if the sentinel has not arrived by then")
	idle := flag.Duration("idle", 5*time.Second, "keep reading this long after the sentinel arrived")
	flag.Parse()
	if *brokers == "" || *out == "" {
		return cerrors.New("-brokers and -out are required")
	}
	if _, err := regexp.Compile(*topicRegex); err != nil {
		return cerrors.Errorf("-topics: %w", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), *timeout)
	defer cancel()

	cl, err := kgo.NewClient(
		kgo.SeedBrokers(strings.Split(*brokers, ",")...),
		kgo.ConsumeRegex(),
		kgo.ConsumeTopics(*topicRegex),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
		// Regex consumption discovers topics through metadata refreshes; the
		// default maximum age (5m) would miss topics created during capture.
		kgo.MetadataMinAge(250*time.Millisecond),
		kgo.MetadataMaxAge(time.Second),
	)
	if err != nil {
		return err
	}
	defer cl.Close()

	events, err := consume(ctx, cl, *sentinel, *idle, *timeout)
	if err != nil {
		return err
	}

	sort.Slice(events, func(i, j int) bool {
		a, b := events[i], events[j]
		if a.Topic != b.Topic {
			return a.Topic < b.Topic
		}
		if a.Partition != b.Partition {
			return a.Partition < b.Partition
		}
		return a.Offset < b.Offset
	})

	if err := os.MkdirAll(*out, 0o750); err != nil {
		return err
	}
	if err := writeJSONL(filepath.Join(*out, "events.jsonl"), events); err != nil {
		return err
	}
	if *registry != "" {
		if err := captureSchemas(ctx, *registry, events, filepath.Join(*out, "schemas.json")); err != nil {
			return err
		}
	}
	fmt.Printf("captured %d events into %s\n", len(events), *out)
	return nil
}

// consume reads until the sentinel topic's first message arrived and then for
// idle longer, so messages written just before the sentinel are included.
func consume(ctx context.Context, cl *kgo.Client, sentinel string, idle, timeout time.Duration) ([]Event, error) {
	var events []Event
	var sawSentinelAt time.Time
	for sawSentinelAt.IsZero() || time.Since(sawSentinelAt) <= idle {
		pollCtx, pollCancel := context.WithTimeout(ctx, time.Second)
		fetches := cl.PollFetches(pollCtx)
		pollCancel()
		if ctx.Err() != nil {
			return nil, cerrors.Errorf("sentinel topic %q not seen within %s (%d events read)", sentinel, timeout, len(events))
		}
		for _, fe := range fetches.Errors() {
			if cerrors.Is(fe.Err, context.DeadlineExceeded) || cerrors.Is(fe.Err, context.Canceled) {
				continue
			}
			return nil, cerrors.Errorf("fetch %s[%d]: %w", fe.Topic, fe.Partition, fe.Err)
		}
		fetches.EachRecord(func(r *kgo.Record) {
			ev := Event{
				Topic:     r.Topic,
				Partition: r.Partition,
				Offset:    r.Offset,
				Key:       newBytes(r.Key),
				Value:     newBytes(r.Value),
			}
			for _, h := range r.Headers {
				ev.Headers = append(ev.Headers, Header{Key: h.Key, Value: newBytes(h.Value)})
			}
			events = append(events, ev)
			if r.Topic == sentinel && sawSentinelAt.IsZero() {
				sawSentinelAt = time.Now()
			}
		})
	}
	return events, nil
}

func writeJSONL(path string, events []Event) error {
	f, err := os.Create(path)
	if err != nil {
		return err
	}
	enc := json.NewEncoder(f)
	enc.SetEscapeHTML(false)
	for _, ev := range events {
		if err := enc.Encode(ev); err != nil {
			_ = f.Close()
			return err
		}
	}
	return f.Close()
}

// Schemas is the registry state the golden events reference.
type Schemas struct {
	// Subjects maps a subject to its versions in registration order.
	Subjects map[string][]SubjectVersion `json:"subjects"`
	// IDs maps every schema ID found in a message's wire header to its schema.
	IDs map[int]string `json:"ids"`
}

// SubjectVersion is one registered version of a subject.
type SubjectVersion struct {
	Version int    `json:"version"`
	ID      int    `json:"id"`
	Schema  string `json:"schema"`
}

func captureSchemas(ctx context.Context, url string, events []Event, path string) error {
	s := Schemas{Subjects: map[string][]SubjectVersion{}, IDs: map[int]string{}}

	var subjects []string
	if err := getJSON(ctx, url+"/subjects", &subjects); err != nil {
		return err
	}
	sort.Strings(subjects)
	for _, subj := range subjects {
		var versions []int
		if err := getJSON(ctx, url+"/subjects/"+subj+"/versions", &versions); err != nil {
			return err
		}
		for _, v := range versions {
			var sv struct {
				Version int    `json:"version"`
				ID      int    `json:"id"`
				Schema  string `json:"schema"`
			}
			if err := getJSON(ctx, fmt.Sprintf("%s/subjects/%s/versions/%d", url, subj, v), &sv); err != nil {
				return err
			}
			s.Subjects[subj] = append(s.Subjects[subj], SubjectVersion{Version: sv.Version, ID: sv.ID, Schema: sv.Schema})
		}
	}

	ids := map[int]bool{}
	// Avro wire-format bytes are often valid UTF-8 (short keys always are),
	// so they can be stored in either form; read both.
	collect := func(b *Bytes) error {
		if b == nil {
			return nil
		}
		var raw []byte
		switch {
		case b.Text != nil:
			raw = []byte(*b.Text)
		case b.Base64 != nil:
			var err error
			if raw, err = base64.StdEncoding.DecodeString(*b.Base64); err != nil {
				return err
			}
		}
		if len(raw) >= 5 && raw[0] == 0 {
			ids[int(binary.BigEndian.Uint32(raw[1:5]))] = true
		}
		return nil
	}
	for _, ev := range events {
		if err := collect(ev.Key); err != nil {
			return err
		}
		if err := collect(ev.Value); err != nil {
			return err
		}
	}
	for id := range ids {
		var sch struct {
			Schema string `json:"schema"`
		}
		if err := getJSON(ctx, fmt.Sprintf("%s/schemas/ids/%d", url, id), &sch); err != nil {
			return err
		}
		s.IDs[id] = sch.Schema
	}

	b, err := json.MarshalIndent(s, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(b, '\n'), 0o600)
}

func getJSON(ctx context.Context, url string, v any) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return err
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return err
	}
	if resp.StatusCode != http.StatusOK {
		return cerrors.Errorf("GET %s: %s: %s", url, resp.Status, body)
	}
	return json.Unmarshal(body, v)
}

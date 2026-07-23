//go:build templates_e2e

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

// End-to-end tests for the two infra-dependent vendored templates
// (postgres-s3, postgres-cdc-kafka):
// docs/design-documents/20260723-templates-gallery.md §6 AC-3. Unlike
// template_gallery_e2e_test.go's generator-sourced templates, these need
// real infra — a real Postgres (with logical replication enabled) plus a
// real S3-compatible store (MinIO) or a real Kafka broker — spun up via
// test/compose-templates.yaml and `make test-integration-templates`. This
// follows the SAME docker-compose + build-tag pattern
// test/compose-postgres.yaml and the `integration` tag already established
// for this repo's other integration tests (Makefile's test-integration
// target) — but under its own tag, `templates_e2e`, and its own Makefile
// target/compose file, rather than reusing `integration` and `./...`: this
// file's infra (dedicated Postgres with logical replication enabled, MinIO,
// Kafka on host-reachable ports) is disjoint from what `make
// test-integration` already provisions, so reusing the shared tag would
// make the existing test-integration job try to exercise these tests
// against infra it never starts.
//
// # Why this is a separate compose file and Makefile target
//
// test/compose-postgres.yaml's Postgres is reused by other, already-green
// integration tests as-is; enabling wal_level=logical on it (required by
// the postgres-cdc-kafka template's forced cdcMode=logrepl) is a container
// restart-policy change this workstream has no business making to unrelated
// tests. A dedicated compose file (its own Postgres, on its own port) keeps
// this workstream's infra changes fully isolated.
//
// # The MinIO virtual-hosted-addressing assumption
//
// conduit-connector-s3's destination builds its client via plain
// s3.NewFromConfig(awsConfig) (destination/writer/s3.go) — no
// UsePathStyle override hook exists, so once AWS_ENDPOINT_URL points it at
// MinIO, it addresses the bucket via virtual-hosted style
// (https://<bucket>.<endpoint-host>), not path style. This test's bucket
// name is DNS-safe and the endpoint host is "localhost:9110", so the
// request host becomes "<bucket>.localhost:9110" — resolving that requires
// "*.localhost" to resolve to 127.0.0.1, which is RFC 6761 behavior most
// modern resolvers (glibc/systemd-resolved on Linux, including GitHub
// Actions' ubuntu-latest runners) implement, and which is the same trick
// localstack's own docs recommend for testing virtual-hosted S3 addressing
// without a wildcard DNS server. This test's own setup/verification calls
// use an explicitly path-style client instead (full control, no such
// dependency) — only the CONNECTOR's traffic (inside the real engine this
// test drives) relies on the assumption.
package pipelines_test

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/jackc/pgx/v5"
	"github.com/matryer/is"
	"github.com/twmb/franz-go/pkg/kgo"
)

const (
	templatesE2EPostgresURL  = "postgres://conduituser:conduitpass@localhost:5442/conduitdb?sslmode=disable"
	templatesE2EMinioURL     = "http://localhost:9110"
	templatesE2EMinioKey     = "conduitminio"
	templatesE2EMinioSecret  = "conduitminiosecret"
	templatesE2EMinioRegion  = "us-east-1"
	templatesE2EKafkaBrokers = "localhost:9095"
)

// postgresSourceURLPlaceholder is the literal value of the postgres-s3 and
// postgres-cdc-kafka templates' source `url:` setting as scaffolded — the
// same string conduit-connector-postgres's source config reads at runtime
// (source/config.go's URL field, tagged json:"url"). Both templates ALSO
// show this same string in an explanatory comment directly above the
// setting (see templates/postgres-s3/pipeline.yaml and
// templates/postgres-cdc-kafka/pipeline.yaml), so it appears twice in the
// scaffolded file: once in a comment, once as the actual value.
const postgresSourceURLPlaceholder = "postgres://user:password@localhost:5432/dbname?sslmode=disable"

// patchPostgresSourceURL replaces the postgres source connector's
// RUNTIME-EFFECTIVE `url:` setting with dsn, and fails the test if it can't
// prove that setting (not just some matching string anywhere in the file)
// actually changed.
//
// Why this isn't a plain strings.Replace(edited, postgresSourceURLPlaceholder, dsn, 1):
// both templates repeat postgresSourceURLPlaceholder in an example comment
// immediately above the real `url:` line. A bare, count-1 replace matches
// whichever occurrence comes first in the file — the comment — and leaves
// the actual setting the connector reads untouched. The connector then
// dials the placeholder host/port/user/db, the pipeline never produces a
// row, and the original `is.True(edited != original)` guard passed anyway
// because the comment text did change. Anchoring on the `url: ` key prefix
// disambiguates the two occurrences and targets only the real setting.
func patchPostgresSourceURL(t *testing.T, edited, dsn string) string {
	t.Helper()
	placeholderSetting := "url: " + postgresSourceURLPlaceholder
	if strings.Count(edited, placeholderSetting) != 1 {
		t.Fatalf("expected exactly one %q settings line in the scaffolded template, found %d — "+
			"template YAML shape changed, update this test's patching logic",
			placeholderSetting, strings.Count(edited, placeholderSetting))
	}
	effectiveSetting := "url: " + dsn
	edited = strings.Replace(edited, placeholderSetting, effectiveSetting, 1)
	if !strings.Contains(edited, effectiveSetting) {
		t.Fatalf("runtime-effective postgres source `url:` setting was not patched to %q", dsn)
	}
	if strings.Contains(edited, placeholderSetting) {
		t.Fatalf("placeholder postgres source `url:` setting is still present after patching")
	}
	return edited
}

// pathStyleS3Client returns an S3 client this TEST fully controls (path
// style, talking directly to MinIO) for setup (bucket creation) and
// verification — deliberately separate from whatever client the pipeline's
// s3 destination connector builds internally.
func pathStyleS3Client(t *testing.T) *s3.Client {
	t.Helper()
	cfg, err := awsconfig.LoadDefaultConfig(
		context.Background(),
		awsconfig.WithRegion(templatesE2EMinioRegion),
		awsconfig.WithCredentialsProvider(credentials.NewStaticCredentialsProvider(
			templatesE2EMinioKey, templatesE2EMinioSecret, "",
		)),
	)
	if err != nil {
		t.Fatalf("load aws config: %v", err)
	}
	return s3.NewFromConfig(cfg, func(o *s3.Options) {
		o.BaseEndpoint = &[]string{templatesE2EMinioURL}[0]
		o.UsePathStyle = true
	})
}

// TestTemplateGalleryE2E_Integration_PostgresS3 is
// docs/design-documents/20260723-templates-gallery.md §6 AC-3 for
// the postgres-s3 template: seed a real Postgres table, scaffold the
// template for real, edit its placeholder connection values (the same edit
// its README instructs a user to make), boot the REAL engine
// (pkg/conduit.Runtime), and assert the seeded rows actually land as
// objects in a real (MinIO-backed) S3 bucket — not just that the YAML
// parses.
func TestTemplateGalleryE2E_Integration_PostgresS3(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping infra-backed template end-to-end test in -short mode")
	}
	is := is.New(t)
	ctx := context.Background()

	// --- seed Postgres ---
	conn, err := pgx.Connect(ctx, templatesE2EPostgresURL)
	if err != nil {
		t.Fatalf("connect to templates-postgres (is `make test-integration-templates` infra up?): %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "DROP TABLE IF EXISTS my_table")
	is.NoErr(err)
	_, err = conn.Exec(ctx, "CREATE TABLE my_table (id INT PRIMARY KEY, name TEXT NOT NULL)")
	is.NoErr(err)
	for i, name := range []string{"alice", "bob", "carol"} {
		_, err = conn.Exec(ctx, "INSERT INTO my_table (id, name) VALUES ($1, $2)", i+1, name)
		is.NoErr(err)
	}

	// --- create the destination bucket (path-style, this test's own client) ---
	bucket := "conduit-templates-e2e-postgres-s3"
	s3Client := pathStyleS3Client(t)
	_, err = s3Client.CreateBucket(ctx, &s3.CreateBucketInput{Bucket: &bucket})
	if err != nil && !strings.Contains(err.Error(), "BucketAlreadyOwnedByYou") && !strings.Contains(err.Error(), "BucketAlreadyExists") {
		t.Fatalf("create bucket: %v", err)
	}

	// --- scaffold the template and edit its placeholders ---
	tmp := t.TempDir()
	pipelinesDir := filepath.Join(tmp, "pipelines")
	is.NoErr(os.MkdirAll(pipelinesDir, 0o755))
	pipelinePath := scaffoldTemplate(t, pipelinesDir, "postgres-s3")

	original, err := os.ReadFile(pipelinePath)
	is.NoErr(err)
	edited := string(original)
	edited = patchPostgresSourceURL(t, edited, templatesE2EPostgresURL)
	edited = strings.Replace(edited, "your-access-key-id", templatesE2EMinioKey, 1)
	edited = strings.Replace(edited, "your-secret-access-key", templatesE2EMinioSecret, 1)
	edited = strings.Replace(edited, "your-bucket-name", bucket, 1)
	is.True(edited != string(original))
	is.NoErr(os.WriteFile(pipelinePath, []byte(edited), 0o600))

	// --- point the s3 destination's AWS client at MinIO ---
	// See this file's doc comment for the virtual-hosted-addressing
	// assumption this relies on.
	is.NoErr(os.Setenv("AWS_ENDPOINT_URL", templatesE2EMinioURL))
	defer os.Unsetenv("AWS_ENDPOINT_URL")

	cfg := conduit.DefaultConfig()
	cfg.DB.Badger.Path = filepath.Join(tmp, "conduit.db")
	cfg.API.Enabled = false
	cfg.Pipelines.Path = pipelinesDir

	r, err := conduit.NewRuntime(cfg)
	is.NoErr(err)

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- r.Run(runCtx) }()

	select {
	case <-r.Ready:
	case err := <-runDone:
		t.Fatalf("runtime exited before becoming ready: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("runtime did not become ready in time")
	}

	// --- assert the seeded rows landed in the bucket ---
	var listOut *s3.ListObjectsV2Output
	deadline := time.Now().Add(30 * time.Second)
	for {
		listOut, err = s3Client.ListObjectsV2(ctx, &s3.ListObjectsV2Input{Bucket: &bucket})
		if err == nil && len(listOut.Contents) > 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("no objects landed in bucket %q within the deadline (last err: %v)", bucket, err)
		}
		time.Sleep(200 * time.Millisecond)
	}

	foundNames := map[string]bool{}
	for _, obj := range listOut.Contents {
		getOut, err := s3Client.GetObject(ctx, &s3.GetObjectInput{Bucket: &bucket, Key: obj.Key})
		is.NoErr(err)
		body, err := io.ReadAll(getOut.Body)
		getOut.Body.Close()
		is.NoErr(err)
		for _, name := range []string{"alice", "bob", "carol"} {
			if strings.Contains(string(body), name) {
				foundNames[name] = true
			}
		}
	}
	is.Equal(len(foundNames), 3) // all three seeded rows landed in S3

	cancel()
	select {
	case <-runDone:
	case <-time.After(20 * time.Second):
		t.Fatal("runtime did not shut down after context cancellation")
	}
}

// TestTemplateGalleryE2E_Integration_PostgresCDCKafka is
// docs/design-documents/20260723-templates-gallery.md §6
// AC-3 for the postgres-cdc-kafka template: scaffold it for real, edit its
// placeholder connection values, boot the real engine BEFORE inserting any
// rows (this template's snapshotMode is "never" — it is CDC-only, per its
// own README, so there is nothing to see until a change happens after
// startup), insert rows into Postgres, and assert those changes actually
// arrive as real messages on a real Kafka topic.
func TestTemplateGalleryE2E_Integration_PostgresCDCKafka(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping infra-backed template end-to-end test in -short mode")
	}
	is := is.New(t)
	ctx := context.Background()

	conn, err := pgx.Connect(ctx, templatesE2EPostgresURL)
	if err != nil {
		t.Fatalf("connect to templates-postgres (is `make test-integration-templates` infra up?): %v", err)
	}
	defer conn.Close(ctx)

	_, err = conn.Exec(ctx, "DROP TABLE IF EXISTS my_table")
	is.NoErr(err)
	_, err = conn.Exec(ctx, "CREATE TABLE my_table (id INT PRIMARY KEY, name TEXT NOT NULL)")
	is.NoErr(err)

	tmp := t.TempDir()
	pipelinesDir := filepath.Join(tmp, "pipelines")
	is.NoErr(os.MkdirAll(pipelinesDir, 0o755))
	pipelinePath := scaffoldTemplate(t, pipelinesDir, "postgres-cdc-kafka")

	topic := "conduit-templates-e2e-postgres-cdc-kafka-" + strconv.FormatInt(time.Now().UnixNano(), 10)

	original, err := os.ReadFile(pipelinePath)
	is.NoErr(err)
	edited := string(original)
	edited = patchPostgresSourceURL(t, edited, templatesE2EPostgresURL)
	edited = strings.Replace(edited, "servers: localhost:9092", "servers: "+templatesE2EKafkaBrokers, 1)
	edited = strings.Replace(edited, "topic: postgres-cdc", "topic: "+topic, 1)
	is.True(edited != string(original))
	is.NoErr(os.WriteFile(pipelinePath, []byte(edited), 0o600))

	cfg := conduit.DefaultConfig()
	cfg.DB.Badger.Path = filepath.Join(tmp, "conduit.db")
	cfg.API.Enabled = false
	cfg.Pipelines.Path = pipelinesDir

	r, err := conduit.NewRuntime(cfg)
	is.NoErr(err)

	runCtx, cancel := context.WithCancel(context.Background())
	defer cancel()

	runDone := make(chan error, 1)
	go func() { runDone <- r.Run(runCtx) }()

	select {
	case <-r.Ready:
	case err := <-runDone:
		t.Fatalf("runtime exited before becoming ready: %v", err)
	case <-time.After(20 * time.Second):
		t.Fatal("runtime did not become ready in time")
	}

	// Give the source time to establish logical replication before the
	// change happens, so this genuinely exercises CDC (not a snapshot).
	time.Sleep(3 * time.Second)

	for i, name := range []string{"dave", "erin"} {
		_, err = conn.Exec(ctx, "INSERT INTO my_table (id, name) VALUES ($1, $2)", i+100, name)
		is.NoErr(err)
	}

	kcl, err := kgo.NewClient(
		kgo.SeedBrokers(templatesE2EKafkaBrokers),
		kgo.ConsumeTopics(topic),
		kgo.ConsumeResetOffset(kgo.NewOffset().AtStart()),
	)
	is.NoErr(err)
	defer kcl.Close()

	found := map[string]bool{}
	deadline := time.Now().Add(30 * time.Second)
	for len(found) < 2 && time.Now().Before(deadline) {
		fetchCtx, fetchCancel := context.WithTimeout(ctx, 5*time.Second)
		fetches := kcl.PollFetches(fetchCtx)
		fetchCancel()
		fetches.EachRecord(func(rec *kgo.Record) {
			for _, name := range []string{"dave", "erin"} {
				if strings.Contains(string(rec.Value), name) {
					found[name] = true
				}
			}
		})
	}
	is.Equal(len(found), 2) // both post-startup changes arrived via CDC

	cancel()
	select {
	case <-runDone:
	case <-time.After(20 * time.Second):
		t.Fatal("runtime did not shut down after context cancellation")
	}
}

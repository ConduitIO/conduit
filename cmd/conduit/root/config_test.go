package root

import (
	"bytes"
	"io"
	"os"
	"reflect"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/conduit"
	"github.com/matryer/is"
)

type TestStruct struct {
	Name   string        `long:"name"`
	Age    int           `long:"age"`
	Active bool          `long:"active"`
	Nested *NestedStruct `long:"nested"`
}

type NestedStruct struct {
	City string `long:"city"`
}

func TestPrintStruct(t *testing.T) {
	is := is.New(t)

	cfg := conduit.DefaultConfig()

	oldStdout := os.Stdout
	r, w, _ := os.Pipe()
	os.Stdout = w

	printStruct(reflect.ValueOf(cfg), "")

	err := w.Close()
	is.NoErr(err)
	os.Stdout = oldStdout

	var buf bytes.Buffer
	_, err = io.Copy(&buf, r)
	is.NoErr(err)

	output := buf.String()

	expectedLines := []string{
		"db.type: badger",
		"db.postgres.table: conduit_kv_store",
		"db.sqlite.table: conduit_kv_store",
		"api.enabled: true",
		"http.address: :8080",
		"grpc.address: :8084",
		"log.level: info",
		"log.format: cli",
		"pipelines.exit-on-degraded: false",
		"pipelines.error-recovery.min-delay: 1s",
		"pipelines.error-recovery.max-delay: 10m0s",
		"pipelines.error-recovery.backoff-factor: 2",
		"pipelines.error-recovery.max-retries: -1",
		"pipelines.error-recovery.max-retries-window: 5m0s",
		"schema-registry.type: builtin",
		"preview.pipeline-arch-v2: false",
	}

	for _, line := range expectedLines {
		is.True(strings.Contains(output, line))
	}
}

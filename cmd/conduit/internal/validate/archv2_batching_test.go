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

package validate

import (
	"context"
	"strings"
	"testing"

	"github.com/conduitio/conduit/pkg/foundation/cerrors/conduiterr"
	lifecycle "github.com/conduitio/conduit/pkg/lifecycle-poc"
	"github.com/conduitio/conduit/pkg/provisioning/config"
	"github.com/matryer/is"
)

const archV2Pipeline = `version: 2.2
pipelines:
  - id: p
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
      - id: dst
        type: destination
        plugin: builtin:log
        settings:
          sdk.batch.size: "5"
    dead-letter-queue:
      plugin: builtin:log
      settings:
        sdk.batch.size: "10"
`

// TestArchV2BatchingFindings pins the arch-v2 refusal surfacing offline: with
// ArchV2 set, a DLQ that batches is refused with the same code the runtime
// engine uses, and a stable config path into the document. A batched
// destination is not refused: the engine batches it itself.
func TestArchV2BatchingFindings(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	report := RunBytes(ctx, "p.yaml", []byte(archV2Pipeline), Options{ArchV2: true})
	is.Equal(len(report.Files), 1)
	fs := report.Files[0]

	var got []Finding
	for _, f := range fs.Findings {
		if f.Code == codeArchV2DestinationBatchingUnsupported {
			got = append(got, f)
		}
	}
	is.Equal(len(got), 1)

	byPath := map[string]Finding{}
	for _, f := range got {
		byPath[f.ConfigPath] = f
		is.Equal(f.Severity, SeverityError)
		is.Equal(f.Suggestion, "set sdk.batch.size to 1 or remove it, or run the pipeline without architecture v2")
		is.True(f.Fix != nil)
		is.Equal(f.Fix.Op, "remove")
		is.Equal(f.Fix.ConfigPath, f.ConfigPath)
	}

	_, ok := byPath["/pipelines/connectors/1/settings/sdk.batch.size"]
	is.True(!ok) // the destination's sdk.batch.size=5 is not refused

	dlq, ok := byPath["/pipelines/dead-letter-queue/settings/sdk.batch.size"]
	is.True(ok)
	is.True(dlq.Message != "")
}

// TestArchV2BatchingFindings_Off is the gate: without ArchV2 the same document
// passes, so `pipelines validate` (which is errors-only and arch-v2-unaware by
// default) does not start failing every batched destination.
func TestArchV2BatchingFindings_Off(t *testing.T) {
	is := is.New(t)
	ctx := context.Background()

	report := RunBytes(ctx, "p.yaml", []byte(archV2Pipeline), Options{})
	is.Equal(len(report.Files), 1)
	for _, f := range report.Files[0].Findings {
		is.True(f.Code != codeArchV2DestinationBatchingUnsupported)
	}
}

// TestArchV2BatchingFindings_IgnoredValues mirrors the shared predicate: 1,
// empty, and unparsable values are not refused (the plugin's own validation
// owns the unparsable case).
func TestArchV2BatchingFindings_IgnoredValues(t *testing.T) {
	for _, tc := range []struct {
		name  string
		value string
	}{
		{"one", `sdk.batch.size: "1"`},
		{"empty", `sdk.batch.size: ""`},
		{"unparsable", `sdk.batch.size: "lots"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			is := is.New(t)
			ctx := context.Background()
			src := `version: 2.2
pipelines:
  - id: p
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
      - id: dst
        type: destination
        plugin: builtin:log
        settings:
` + "          " + tc.value + "\n"

			report := RunBytes(ctx, "p.yaml", []byte(src), Options{ArchV2: true})
			for _, f := range report.Files[0].Findings {
				is.True(f.Code != codeArchV2DestinationBatchingUnsupported)
			}
		})
	}
}

// TestArchV2BatchingFindings_DLQDelay (#3321): the runtime guard refuses a DLQ
// sdk.batch.delay above 0 (the SDK batches on a delay alone), and the offline
// check reports the same code for it, so validate and doctor agree with
// `conduit run`.
func TestArchV2BatchingFindings_DLQDelay(t *testing.T) {
	for name, tc := range map[string]struct {
		delay string
		want  bool
	}{
		"1s":         {delay: "1s", want: true},
		"0":          {delay: "0"},
		"unparsable": {delay: "soon"},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			doc := `version: 2.2
pipelines:
  - id: p
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
      - id: dst
        type: destination
        plugin: builtin:log
    dead-letter-queue:
      plugin: builtin:log
      settings:
        sdk.batch.delay: "` + tc.delay + `"
`
			report := RunBytes(context.Background(), "p.yaml", []byte(doc), Options{ArchV2: true})
			is.Equal(len(report.Files), 1)
			var got []Finding
			for _, f := range report.Files[0].Findings {
				if f.Code == codeArchV2DestinationBatchingUnsupported {
					got = append(got, f)
				}
			}
			if !tc.want {
				is.Equal(len(got), 0)
				return
			}
			is.Equal(len(got), 1)
			is.Equal(got[0].ConfigPath, "/pipelines/dead-letter-queue/settings/sdk.batch.delay")
			is.Equal(got[0].Fix.Op, "remove")
		})
	}
}

// TestArchV2BatchingFindings_NotLiteral (#3321 review): a destination or DLQ
// sdk.batch.* value that is not a literal is refused at start under arch-v2,
// so validate reports it offline with the same code and the setting's path.
func TestArchV2BatchingFindings_NotLiteral(t *testing.T) {
	doc := `version: 2.2
pipelines:
  - id: p
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
        settings:
          sdk.batch.size: "${SRC_BATCH}"
      - id: dst
        type: destination
        plugin: builtin:log
        settings:
          sdk.batch.size: "${S3_BATCH}"
    dead-letter-queue:
      plugin: builtin:log
      settings:
        sdk.batch.delay: "${DLQ_DELAY}"
`
	is := is.New(t)
	report := RunBytes(context.Background(), "p.yaml", []byte(doc), Options{ArchV2: true})
	is.Equal(len(report.Files), 1)
	var got []Finding
	for _, f := range report.Files[0].Findings {
		if f.Code == codeArchV2DestinationBatchingNotLiteral {
			got = append(got, f)
		}
	}
	is.Equal(len(got), 2) // the destination and the DLQ; a source is not engine-batched
	is.Equal(got[0].ConfigPath, "/pipelines/connectors/1/settings/sdk.batch.size")
	is.True(strings.Contains(got[0].Message, "sdk.batch.size=${S3_BATCH}"))
	is.True(strings.Contains(got[0].Suggestion, "literal"))
	is.Equal(got[1].ConfigPath, "/pipelines/dead-letter-queue/settings/sdk.batch.delay")

	off := RunBytes(context.Background(), "p.yaml", []byte(doc), Options{ArchV2: false})
	for _, f := range off.Files[0].Findings {
		is.True(f.Code != codeArchV2DestinationBatchingNotLiteral) // classic is unchanged
	}
}

// TestArchV2BatchingFindings_DestinationBatchingNotFlagged (#3321): Start and
// PreflightPipelineConfig do not refuse a destination's sdk.batch.size or
// sdk.batch.delay (the engine batches destinations itself), so the offline
// check must not either.
func TestArchV2BatchingFindings_DestinationBatchingNotFlagged(t *testing.T) {
	is := is.New(t)
	doc := `version: 2.2
pipelines:
  - id: p
    status: running
    connectors:
      - id: src
        type: source
        plugin: builtin:generator
      - id: dst
        type: destination
        plugin: builtin:log
        settings:
          sdk.batch.size: "10000"
          sdk.batch.delay: "1s"
`
	report := RunBytes(context.Background(), "p.yaml", []byte(doc), Options{ArchV2: true})
	is.Equal(len(report.Files), 1)
	for _, f := range report.Files[0].Findings {
		is.True(f.Code != codeArchV2DestinationBatchingUnsupported)
		is.True(f.Code != codeArchV2DestinationBatchingNotLiteral)
	}
}

// TestArchV2BatchingFindings_RuntimeParity checks the offline findings against
// the runtime preflight (the guard Start shares) over a table of settings: the
// preflight refuses exactly when the offline check reports, with the same code.
func TestArchV2BatchingFindings_RuntimeParity(t *testing.T) {
	svc := &lifecycle.Service{}
	for name, tc := range map[string]struct{ dst, dlq map[string]string }{
		"clean":                {},
		"dst batched":          {dst: map[string]string{"sdk.batch.size": "10000", "sdk.batch.delay": "1s"}},
		"dlq size":             {dlq: map[string]string{"sdk.batch.size": "2"}},
		"dlq size 1":           {dlq: map[string]string{"sdk.batch.size": "1"}},
		"dlq delay":            {dlq: map[string]string{"sdk.batch.delay": "1s"}},
		"dlq delay 0":          {dlq: map[string]string{"sdk.batch.delay": "0"}},
		"dst env":              {dst: map[string]string{"sdk.batch.size": "${B}"}},
		"dlq env":              {dlq: map[string]string{"sdk.batch.delay": "${D}"}},
		"dst env + dlq size":   {dst: map[string]string{"sdk.batch.size": "${B}"}, dlq: map[string]string{"sdk.batch.size": "5"}},
		"dst batched, dlq env": {dst: map[string]string{"sdk.batch.size": "50"}, dlq: map[string]string{"sdk.batch.size": "${B}"}},
	} {
		t.Run(name, func(t *testing.T) {
			is := is.New(t)
			render := func(m map[string]string) string {
				out := ""
				for k, v := range m {
					out += "          " + k + ": \"" + v + "\"\n"
				}
				return out
			}
			doc := "version: 2.2\npipelines:\n  - id: p\n    status: running\n    connectors:\n" +
				"      - id: src\n        type: source\n        plugin: builtin:generator\n" +
				"      - id: dst\n        type: destination\n        plugin: builtin:log\n"
			if len(tc.dst) > 0 {
				doc += "        settings:\n" + render(tc.dst)
			}
			doc += "    dead-letter-queue:\n      plugin: builtin:log\n"
			if len(tc.dlq) > 0 {
				doc += "      settings:\n" + render(tc.dlq)
			}

			cfg := config.Pipeline{
				ID: "p",
				Connectors: []config.Connector{
					{ID: "src", Type: config.TypeSource},
					{ID: "dst", Type: config.TypeDestination, Settings: tc.dst},
				},
				DLQ: config.DLQ{Settings: tc.dlq},
			}
			want := ""
			if err := svc.PreflightPipelineConfig(cfg); err != nil {
				ce, ok := conduiterr.Get(err)
				is.True(ok)
				want = ce.Code.Reason()
			}

			report := RunBytes(context.Background(), "p.yaml", []byte(doc), Options{ArchV2: true})
			is.Equal(len(report.Files), 1)
			codes := map[string]bool{}
			for _, f := range report.Files[0].Findings {
				if f.Code == codeArchV2DestinationBatchingUnsupported || f.Code == codeArchV2DestinationBatchingNotLiteral {
					codes[f.Code] = true
				}
			}
			switch want {
			case "":
				is.Equal(len(codes), 0)
			case codeArchV2DestinationBatchingNotLiteral:
				// The runtime reports not-literal first; offline reports both.
				is.True(codes[codeArchV2DestinationBatchingNotLiteral])
			default:
				is.True(codes[want])
				is.True(!codes[codeArchV2DestinationBatchingNotLiteral])
			}
		})
	}
}

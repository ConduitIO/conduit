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

package main

import (
	"strings"
	"testing"
)

// TestRunArgsBindsTheAPIToLoopback is the regression test for the gate run of
// 2026-11-27, where all six sessions failed in four seconds. The image sets
// CONDUIT_API_HTTP_ADDRESS and CONDUIT_API_GRPC_ADDRESS to 0.0.0.0 so a
// published port is reachable, and Conduit refuses to start on a non-loopback
// bind with no API token (`code: api.token_required`). The harness passed no
// API flags, so every run of every arm exited during startup, on both engines.
func TestRunArgsBindsTheAPIToLoopback(t *testing.T) {
	cfg := config{pipelineFile: "/shapes/1x1/pipeline.yml"}
	for _, eng := range []engine{engineV1, engineV2} {
		args := strings.Join(runArgs("img", cfg, runSpec{Engine: eng}), " ")
		for _, want := range []string{
			"--api.http.address 127.0.0.1:8080",
			"--api.grpc.address 127.0.0.1:8084",
		} {
			if !strings.Contains(args, want) {
				t.Errorf("engine %s: args do not contain %q: %s", eng, want, args)
			}
		}
		if strings.Contains(args, "0.0.0.0") {
			t.Errorf("engine %s: args bind the API off loopback: %s", eng, args)
		}
	}
}

// TestRunArgsDifferOnlyByTheEngineFlag pins the harness's central claim: both
// arms run the same image and the same pipeline file, and the only difference
// is --preview.pipeline-arch-v2. A second difference would make every delta
// unattributable.
func TestRunArgsDifferOnlyByTheEngineFlag(t *testing.T) {
	cfg := config{pipelineFile: "/shapes/2x2/pipeline.yml"}
	v1 := runArgs("img", cfg, runSpec{Engine: engineV1})
	v2 := runArgs("img", cfg, runSpec{Engine: engineV2})
	if len(v2) != len(v1)+1 {
		t.Fatalf("v2 has %d args and v1 has %d; want exactly one more", len(v2), len(v1))
	}
	for i := range v1 {
		if v1[i] != v2[i] {
			t.Fatalf("arg %d differs: v1 %q, v2 %q", i, v1[i], v2[i])
		}
	}
	if last := v2[len(v2)-1]; last != "--preview.pipeline-arch-v2" {
		t.Fatalf("the extra v2 arg is %q", last)
	}
	if strings.Contains(strings.Join(v1, " "), "--preview.pipeline-arch-v2") {
		t.Fatal("the v1 arm carries the arch-v2 flag")
	}
}

// TestRunArgsCPUsLimit checks the optional --cpus limit reaches docker, and
// only when set.
func TestRunArgsCPUsLimit(t *testing.T) {
	args := strings.Join(runArgs("img", config{pipelineFile: "p.yml", CPUs: "8"}, runSpec{Engine: engineV1}), " ")
	if !strings.Contains(args, "--cpus 8") {
		t.Errorf("--cpus was not passed: %s", args)
	}
	args = strings.Join(runArgs("img", config{pipelineFile: "p.yml"}, runSpec{Engine: engineV1}), " ")
	if strings.Contains(args, "--cpus") {
		t.Errorf("--cpus was passed with no limit configured: %s", args)
	}
}

// TestCombinedCapturesStderr is the regression test for the diagnostics half
// of the same failure: the container's logs were read with a helper that
// returned stdout only, so the report showed Conduit's ASCII banner and threw
// away the error that explained the exit.
func TestCombinedCapturesStderr(t *testing.T) {
	out, err := combined(t.Context(), "sh", "-c", "echo to-stdout; echo to-stderr 1>&2")
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"to-stdout", "to-stderr"} {
		if !strings.Contains(out, want) {
			t.Errorf("combined output is missing %q: %q", want, out)
		}
	}
}

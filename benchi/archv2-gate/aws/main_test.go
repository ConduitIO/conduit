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
	"os/exec"
	"strings"
	"testing"
	"text/template"
	"time"
)

// TestUserData checks the rendered script parses, schedules the hard cap
// before anything else, and fits EC2's 16 KB user-data limit.
func TestUserData(t *testing.T) {
	for _, proto := range []bool{false, true} {
		s := renderUserData(t, proto, "early")
		checkUserData(t, s)
		// Every session counts its sinks with the base image, so it is pulled.
		if !strings.Contains(s, "docker pull -q alpine:3") {
			t.Fatal("user data does not pull the harness base image")
		}
		// The prototype session is only in the script when it is asked for,
		// and the script itself guards it on PROTO_SHA.
		if got := strings.Contains(s, "PROTO_SHA="+strings.Repeat("e", 40)); got != proto {
			t.Fatalf("proto=%v: PROTO_SHA set in env file = %v", proto, got)
		}
		if !strings.Contains(s, `-arm "proto-alta=v2:$BINS/conduit-proto@1x1-srcbatch100"`) {
			t.Fatal("Alternative A session has no prototype source-batch arm")
		}
	}
}

func TestValidateProto(t *testing.T) {
	sha := strings.Repeat("e", 40)
	for _, ok := range [][2]string{{"", ""}, {"2978", sha}} {
		if err := validateProto(ok[0], ok[1]); err != nil {
			t.Errorf("validateProto(%q, %q): %v", ok[0], ok[1], err)
		}
	}
	for _, bad := range [][2]string{{"2978", ""}, {"", sha}, {"x", sha}, {"2978", "abc"}, {"2978", sha + "; rm -rf /"}} {
		if validateProto(bad[0], bad[1]) == nil {
			t.Errorf("validateProto(%q, %q): want an error", bad[0], bad[1])
		}
	}
}

// TestUserDataMatrixPlan checks the matrix plan: no fan-out PR, run.sh with
// the gate's window, and a re-render at the end.
func TestUserDataMatrixPlan(t *testing.T) {
	s := renderUserData(t, true, "matrix")
	checkUserData(t, s)
	for _, want := range []string{
		"PLAN=matrix\n", "FANOUT_SHA=\n", "WINDOW=1m0s\n",
		"bash benchi/archv2-gate/matrix/run.sh",
		`-rerender "$OUT/results/matrix"`,
	} {
		if !strings.Contains(s, want) {
			t.Errorf("matrix user data lacks %q", want)
		}
	}
}

func renderUserData(t *testing.T, proto bool, plan string) string {
	t.Helper()
	data := map[string]any{
		"MaxMinutes": 360, "Bucket": "b", "Prefix": "runs/x", "Region": "us-west-1",
		"MainSHA": strings.Repeat("a", 40), "FanoutSHA": strings.Repeat("b", 40), "FanoutPR": "2946",
		"HarnessSHA": strings.Repeat("c", 40), "HarnessPR": "2956", "ProtoSHA": "", "ProtoPR": "",
		"Plan": "early", "Warmup": "20s", "Window": "1m0s",
		"GoVersion": "1.25.14", "GoSHA256": strings.Repeat("d", 64), "Rounds": 5,
	}
	if proto {
		data["ProtoSHA"], data["ProtoPR"] = strings.Repeat("e", 40), "2978"
	}
	data["Plan"] = plan
	if plan == "matrix" {
		data["FanoutSHA"], data["FanoutPR"] = "", ""
	}
	var ud strings.Builder
	if err := template.Must(template.New("ud").Parse(userDataTmpl)).Execute(&ud, data); err != nil {
		t.Fatal(err)
	}
	return ud.String()
}

func checkUserData(t *testing.T, s string) {
	t.Helper()
	if len(encodeBase64(s)) > 16*1024 {
		t.Fatalf("user data is %d bytes base64, over the 16 KB limit", len(encodeBase64(s)))
	}
	var firstCmd string
	for _, line := range strings.Split(s, "\n") {
		if line != "" && !strings.HasPrefix(line, "#") {
			firstCmd = line
			break
		}
	}
	if firstCmd != "shutdown -h +360" {
		t.Fatalf("first command is %q, want the hard cap", firstCmd)
	}
	if !strings.Contains(s, "docker version --format '{{.Server.Version}}'") {
		t.Fatal("docker format string was not escaped through the template")
	}
	for _, script := range []string{s, between(s, "<<'SCRIPT'\n", "\nSCRIPT\n")} {
		cmd := exec.CommandContext(t.Context(), "bash", "-n")
		cmd.Stdin = strings.NewReader(script)
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("bash -n: %v\n%s", err, out)
		}
	}
}

func between(s, start, end string) string {
	_, after, _ := strings.Cut(s, start)
	before, _, _ := strings.Cut(after, end)
	return before
}

func TestValidateLaunch(t *testing.T) {
	sha := func(c string) string { return strings.Repeat(c, 40) }
	matrix := launchFlags{
		plan: "matrix", mainSHA: sha("a"), harnessSHA: sha("c"), harnessPR: "3067",
		protoSHA: sha("e"), protoPR: "2978", window: 60 * time.Second, benchTag: "archv2-gate-20261014",
	}
	if err := validateLaunch(matrix); err != nil {
		t.Fatalf("matrix flags: %v", err)
	}
	early := launchFlags{
		plan: "early", mainSHA: sha("a"), harnessSHA: sha("c"), harnessPR: "2956",
		fanoutSHA: sha("b"), fanoutPR: "2946", window: 60 * time.Second, benchTag: "x",
	}
	if err := validateLaunch(early); err != nil {
		t.Fatalf("early flags: %v", err)
	}
	for name, mod := range map[string]func(*launchFlags){
		"matrix without proto": func(f *launchFlags) { f.protoSHA, f.protoPR = "", "" },
		"matrix with fan-out":  func(f *launchFlags) { f.fanoutSHA, f.fanoutPR = sha("b"), "2946" },
		"short window":         func(f *launchFlags) { f.window = 25 * time.Second },
		"unknown plan":         func(f *launchFlags) { f.plan = "all" },
		"tag with spaces":      func(f *launchFlags) { f.benchTag = "a b" },
		"short main sha":       func(f *launchFlags) { f.mainSHA = "abc" },
		"harness pr not digit": func(f *launchFlags) { f.harnessPR = "x" },
	} {
		f := matrix
		mod(&f)
		if validateLaunch(f) == nil {
			t.Errorf("%s: want an error", name)
		}
	}
	early.fanoutSHA = ""
	if validateLaunch(early) == nil {
		t.Error("early without fan-out: want an error")
	}
}

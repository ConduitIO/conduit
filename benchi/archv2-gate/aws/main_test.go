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
)

// TestUserData checks the rendered script parses, schedules the hard cap
// before anything else, and fits EC2's 16 KB user-data limit.
func TestUserData(t *testing.T) {
	var ud strings.Builder
	err := template.Must(template.New("ud").Parse(userDataTmpl)).Execute(&ud, map[string]any{
		"MaxMinutes": 360, "Bucket": "b", "Prefix": "runs/x", "Region": "us-west-1",
		"MainSHA": strings.Repeat("a", 40), "FanoutSHA": strings.Repeat("b", 40), "FanoutPR": "2946",
		"HarnessSHA": strings.Repeat("c", 40), "HarnessPR": "2956",
		"GoVersion": "1.25.14", "GoSHA256": strings.Repeat("d", 64), "Rounds": 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	s := ud.String()
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

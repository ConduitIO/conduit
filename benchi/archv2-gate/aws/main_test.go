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
	"context"
	"errors" //nolint:depguard // separate module; cannot import Conduit cerrors
	"os"
	"os/exec"
	"regexp"
	"strings"
	"testing"
	"text/template"
	"time"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
)

// gateData is the user data of a gate run: the harness and both engines come
// from main, and no PR is measured.
func gateData() map[string]any {
	return map[string]any{
		"MaxMinutes": 360, "Bucket": "b", "Prefix": "runs/x", "Region": "us-west-1",
		"MainSHA": strings.Repeat("a", 40), "FanoutSHA": "", "FanoutPR": "",
		"HarnessSHA": "", "HarnessPR": "",
		"GoVersion": defaultGoVersion, "GoSHA256": strings.Repeat("d", 64), "Rounds": 5,
	}
}

// prData adds both optional PR pairs.
func prData() map[string]any {
	d := gateData()
	d["FanoutSHA"] = strings.Repeat("b", 40)
	d["FanoutPR"] = "2946"
	d["HarnessSHA"] = strings.Repeat("c", 40)
	d["HarnessPR"] = "2956"
	return d
}

func render(t *testing.T, data map[string]any) string {
	t.Helper()
	var ud strings.Builder
	if err := template.Must(template.New("ud").Parse(userDataTmpl)).Execute(&ud, data); err != nil {
		t.Fatal(err)
	}
	return ud.String()
}

// TestUserData checks that both shapes of the script parse, schedule the hard
// cap before anything else, and fit EC2's 16 KB user-data limit.
func TestUserData(t *testing.T) {
	for name, data := range map[string]map[string]any{"gate": gateData(), "with-prs": prData()} {
		t.Run(name, func(t *testing.T) {
			s := render(t, data)
			if n := len(encodeBase64(s)); n > 16*1024 {
				t.Fatalf("user data is %d bytes base64, over the 16 KB limit", n)
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
			// The nine gate sessions run in both shapes.
			for _, want := range []string{
				"session 01-1x1-aa-v1 conduit-bench:main 1x1 aa-v1",
				"session 02-1x1-aa-v2 conduit-bench:main 1x1 aa-v2",
				"session 03-2x2-aa-v1 conduit-bench:main 2x2 aa-v1",
				"session 04-2x2-aa-v2 conduit-bench:main 2x2 aa-v2",
				"session 05-1x1-ab-main conduit-bench:main 1x1 ab",
				"session 06-2x2-ab-main conduit-bench:main 2x2 ab",
				"session 07-4x4-aa-v1 conduit-bench:main 4x4 aa-v1",
				"session 08-4x4-aa-v2 conduit-bench:main 4x4 aa-v2",
				"session 09-4x4-ab-main conduit-bench:main 4x4 ab",
			} {
				if !strings.Contains(s, want) {
					t.Errorf("rendered script is missing %q", want)
				}
			}
			for _, script := range []string{s, between(s, "<<'SCRIPT'\n", "\nSCRIPT\n")} {
				cmd := exec.CommandContext(t.Context(), "bash", "-n")
				cmd.Stdin = strings.NewReader(script)
				if out, err := cmd.CombinedOutput(); err != nil {
					t.Fatalf("bash -n: %v\n%s", err, out)
				}
			}
		})
	}
}

// TestUserDataWithoutPRsTouchesNoPR is the regression test for a launch that
// names no PR: before the pairs became optional the only way to run the gate
// was to name #2946 and #2956, which are both merged, so the script fetched
// dead PR refs and ran a seventh session comparing main with itself.
func TestUserDataWithoutPRsTouchesNoPR(t *testing.T) {
	s := render(t, gateData())
	for _, unwanted := range []string{
		"refs/pull", "src-fanout", "conduit-bench:fanout", "conduit-bench:pr",
		"10-2x2-fanout", "fanout-wrap", "src-harness",
	} {
		if strings.Contains(s, unwanted) {
			t.Errorf("a gate run should not mention %q", unwanted)
		}
	}
	// With no harness PR the harness builds from main's own worktree.
	if !strings.Contains(s, "HARNESS_SRC=$W/src-main") {
		t.Error("the harness source should default to the main worktree")
	}
	if strings.Count(s, "HARNESS_SRC=") != 1 {
		t.Errorf("HARNESS_SRC is set %d times, want once", strings.Count(s, "HARNESS_SRC="))
	}
}

// TestUserDataWithPRsKeepsBothExtras checks the optional pairs still do what
// they did: fetch each PR ref, build the wrapper image, run session 07, and
// build the harness from the harness PR.
func TestUserDataWithPRsKeepsBothExtras(t *testing.T) {
	s := render(t, prData())
	for _, want := range []string{
		`git fetch -q origin "+refs/pull/${FANOUT_PR}/head`,
		`git fetch -q origin "+refs/pull/${HARNESS_PR}/head`,
		`git worktree add -q --detach "$W/src-fanout"`,
		`git worktree add -q --detach "$W/src-harness"`,
		"-t conduit-bench:fanout",
		"session 10-2x2-fanout conduit-bench:fanout 2x2 ab",
		"HARNESS_SRC=$W/src-harness",
	} {
		if !strings.Contains(s, want) {
			t.Errorf("rendered script is missing %q", want)
		}
	}
}

// TestPRPair pins the all-or-nothing rule on each optional pair. A pair given
// half way is a typo, and launching on it would measure the wrong commit or
// fail minutes into the instance's life.
func TestPRPair(t *testing.T) {
	sha := strings.Repeat("a", 40)
	for _, tc := range []struct {
		name, pr, sha string
		wantErr       bool
	}{
		{name: "both empty", pr: "", sha: ""},
		{name: "both set", pr: "2946", sha: sha},
		{name: "pr only", pr: "2946", sha: "", wantErr: true},
		{name: "sha only", pr: "", sha: sha, wantErr: true},
		{name: "pr not a number", pr: "#2946", sha: sha, wantErr: true},
		{name: "short sha", pr: "2946", sha: sha[:7], wantErr: true},
		{name: "upper case sha", pr: "2946", sha: strings.ToUpper(sha), wantErr: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := prPair("x-pr", "x-sha", tc.pr, tc.sha)
			if tc.wantErr != (err != nil) {
				t.Fatalf("prPair(%q, %q) = %v, wantErr %v", tc.pr, tc.sha, err, tc.wantErr)
			}
		})
	}
}

// TestDefaultGoVersionMatchesRootModule guards the drift that made this
// launcher install Go 1.25.14 for a tree whose go.mod asks for 1.26.11.
func TestDefaultGoVersionMatchesRootModule(t *testing.T) {
	raw, err := os.ReadFile("../../../go.mod")
	if err != nil {
		t.Fatal(err)
	}
	m := regexp.MustCompile(`(?m)^go (\d+\.\d+\.\d+)$`).FindStringSubmatch(string(raw))
	if m == nil {
		t.Fatal("no `go <version>` directive in the root go.mod")
	}
	if m[1] != defaultGoVersion {
		t.Fatalf("defaultGoVersion is %s, but the root module needs %s", defaultGoVersion, m[1])
	}
}

func between(s, start, end string) string {
	_, after, _ := strings.Cut(s, start)
	before, _, _ := strings.Cut(after, end)
	return before
}

// TestInstanceStateAbsentMeansGone is the regression test for the teardown of
// 2026-11-27. The instance had self-terminated and EC2 had purged its record,
// so DescribeInstances answered with no reservations rather than an error.
// terminateInstance read that as "terminated" from the zero value, skipped the
// terminate call correctly, and then waited on an InstanceTerminated waiter
// that reads the same empty answer — ten minutes, then
// "exceeded max wait time for InstanceTerminated waiter". cleanup returned on
// that error and never deleted the security group or the IAM role, which held
// PutObject on the results prefix, while still reporting nothing was wrong.
func TestInstanceStateAbsentMeansGone(t *testing.T) {
	for _, tc := range []struct {
		name        string
		out         *ec2.DescribeInstancesOutput
		wantState   ec2types.InstanceStateName
		wantPresent bool
	}{
		{name: "nil answer", out: nil},
		{name: "no reservations", out: &ec2.DescribeInstancesOutput{}},
		{
			name: "reservation with no instances",
			out:  &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{{}}},
		},
		{
			name: "instance with no state",
			out: &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{
				{Instances: []ec2types.Instance{{}}},
			}},
		},
		{
			name: "terminated",
			out: &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{
				{Instances: []ec2types.Instance{{State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameTerminated}}}},
			}},
			wantState: ec2types.InstanceStateNameTerminated, wantPresent: true,
		},
		{
			name: "running",
			out: &ec2.DescribeInstancesOutput{Reservations: []ec2types.Reservation{
				{Instances: []ec2types.Instance{{State: &ec2types.InstanceState{Name: ec2types.InstanceStateNameRunning}}}},
			}},
			wantState: ec2types.InstanceStateNameRunning, wantPresent: true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			state, present := instanceState(tc.out)
			if present != tc.wantPresent || state != tc.wantState {
				t.Fatalf("instanceState = (%q, %v), want (%q, %v)", state, present, tc.wantState, tc.wantPresent)
			}
		})
	}
}

// fakeEC2 counts the calls the instance teardown makes. It answers only the
// first describe: the terminated-waiter polls DescribeInstances, so failing
// every later call makes the waiter give up at once instead of running for its
// full timeout. Without that, a test asserting the waiter was not entered
// takes ten minutes to fail, which is a test nobody will keep.
type fakeEC2 struct {
	out        *ec2.DescribeInstancesOutput
	describes  int
	terminates int
}

func (f *fakeEC2) DescribeInstances(context.Context, *ec2.DescribeInstancesInput, ...func(*ec2.Options)) (*ec2.DescribeInstancesOutput, error) {
	f.describes++
	if f.describes > 1 {
		return nil, errors.New("the waiter polled DescribeInstances again")
	}
	return f.out, nil
}

func (f *fakeEC2) TerminateInstances(context.Context, *ec2.TerminateInstancesInput, ...func(*ec2.Options)) (*ec2.TerminateInstancesOutput, error) {
	f.terminates++
	return &ec2.TerminateInstancesOutput{}, nil
}

// TestTerminateInstanceDoesNotWaitOnAnAbsentInstance is the behavioural
// regression test for the teardown of 2026-11-27. EC2 had purged the
// self-terminated instance, so DescribeInstances answered with no
// reservations. The old code read that as "terminated" from a zero value,
// skipped the terminate call, and then entered the InstanceTerminated waiter,
// which polls the same empty answer until its timeout:
// "exceeded max wait time for InstanceTerminated waiter". cleanup returned
// that error and left the security group and an IAM role holding PutObject in
// place.
//
// The waiter polls DescribeInstances many times over minutes, so one describe
// and no terminate is the signature of the fixed path. A test that only
// checked the parse helper did not catch this: removing the early return left
// it green.
func TestTerminateInstanceDoesNotWaitOnAnAbsentInstance(t *testing.T) {
	f := &fakeEC2{out: &ec2.DescribeInstancesOutput{}}
	start := time.Now()
	if err := terminateInstance(t.Context(), f, "i-0e6d7f02dc9be21f3"); err != nil {
		t.Fatalf("an absent instance should be treated as terminated, got %v", err)
	}
	if f.describes != 1 {
		t.Errorf("DescribeInstances called %d times, want 1 — the waiter was entered", f.describes)
	}
	if f.terminates != 0 {
		t.Errorf("TerminateInstances called %d times on an absent instance, want 0", f.terminates)
	}
	if d := time.Since(start); d > 5*time.Second {
		t.Errorf("took %s; the waiter was entered", d)
	}
}

// TestTerminateInstanceSkipsEverythingWithoutAnID keeps the no-instance case
// free of API calls, so a manifest from a launch that failed before
// RunInstances tears down cleanly.
func TestTerminateInstanceSkipsEverythingWithoutAnID(t *testing.T) {
	f := &fakeEC2{out: &ec2.DescribeInstancesOutput{}}
	if err := terminateInstance(t.Context(), f, ""); err != nil {
		t.Fatal(err)
	}
	if f.describes != 0 || f.terminates != 0 {
		t.Errorf("made %d describes and %d terminates, want none", f.describes, f.terminates)
	}
}

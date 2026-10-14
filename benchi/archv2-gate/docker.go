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
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

// containerName and volumeName carry the harness's PID so two sessions on one
// Docker daemon, or a session and someone else's container, never remove each
// other's.
var (
	containerName = fmt.Sprintf("archv2-gate-conduit-%d", os.Getpid())
	volumeName    = fmt.Sprintf("archv2-gate-sink-%d", os.Getpid())
)

const (
	// sinkReadyTimeout bounds how long a run waits for every sink file to
	// exist and hold at least one record before giving up.
	sinkReadyTimeout = 90 * time.Second
)

type docker struct {
	// image holds the Conduit binary for arms that do not bring their own.
	image string
	// baseImage runs arms that bring a binary, and counts the sinks.
	baseImage string
}

// imageFor returns the image that runs spec.
func (d docker) imageFor(spec runSpec) string {
	if spec.Binary != "" {
		return d.baseImage
	}
	return d.image
}

func (d docker) cmd(ctx context.Context, args ...string) (string, error) {
	c := exec.CommandContext(ctx, "docker", args...)
	var stdout, stderr bytes.Buffer
	c.Stdout, c.Stderr = &stdout, &stderr
	if err := c.Run(); err != nil {
		return stdout.String(), cerrors.Errorf("docker %s: %w: %s", args[0], err, strings.TrimSpace(stderr.String()))
	}
	return stdout.String(), nil
}

// preflight checks the images the session needs exist and starts from a clean
// container and sink volume.
func (d docker) preflight(ctx context.Context, cfg config) error {
	need := map[string]bool{d.baseImage: true}
	if cfg.Session != sessionMatrix || slices.ContainsFunc(cfg.Arms, func(a armSpec) bool { return a.Binary == "" }) {
		need[d.image] = true
	}
	for img := range need {
		if _, err := d.cmd(ctx, "image", "inspect", img); err != nil {
			return cerrors.Errorf("image %q not found; build or pull it first (see README.md): %w", img, err)
		}
	}
	d.cleanup()
	if _, err := d.cmd(ctx, "volume", "create", volumeName); err != nil {
		return err
	}
	return nil
}

// cleanup removes the container and sink volume. It uses its own context so
// it still runs after an interrupt cancelled the session's.
func (d docker) cleanup() {
	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	_, _ = d.cmd(ctx, "rm", "-f", containerName)
	_, _ = d.cmd(ctx, "volume", "rm", "-f", volumeName)
}

type runResult struct {
	runSpec
	Started     time.Time
	WindowSec   float64
	SinkRecords []int64
	SinkBytes   []int64
	// Rate is records per second per sink: the mean over sinks of
	// records counted in the window divided by the window length. In a 2x2
	// shape every record reaches both sinks, so this is the end-to-end rate.
	Rate         float64
	HostLoad     string
	VMLoadBefore string
	VMLoadAfter  string
}

// measure performs one single run. See the package doc for the method.
func (d docker) measure(ctx context.Context, cfg config, spec runSpec) (runResult, error) {
	cfg = cfg.forSpec(spec)
	res := runResult{runSpec: spec, Started: time.Now().UTC(), HostLoad: hostLoad(ctx)}

	_, _ = d.cmd(ctx, "rm", "-f", containerName)
	if _, err := d.start(ctx, cfg, spec); err != nil {
		return res, err
	}
	defer func() {
		ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
		defer cancel()
		_, _ = d.cmd(ctx, "rm", "-f", containerName)
	}()

	if err := d.waitForSinks(ctx, cfg.sinks); err != nil {
		return res, err
	}
	if err := sleep(ctx, cfg.Warmup); err != nil {
		return res, err
	}

	res.VMLoadBefore = d.vmLoad(ctx)
	s0, t0, err := d.sinkSizes(ctx, cfg.sinks)
	if err != nil {
		return res, err
	}
	if err := sleep(ctx, cfg.Window); err != nil {
		return res, err
	}
	s1, t1, err := d.sinkSizes(ctx, cfg.sinks)
	if err != nil {
		return res, err
	}
	res.VMLoadAfter = d.vmLoad(ctx)
	if err := d.assertRunning(ctx); err != nil {
		return res, cerrors.Errorf("conduit stopped during the window, run is invalid: %w", err)
	}
	res.WindowSec = t1.Sub(t0).Seconds()
	if err := d.attestEngine(ctx, spec); err != nil {
		return res, err
	}

	// Stop writing before counting so the count does not compete with the
	// pipeline for the machine; the byte range is already fixed.
	if _, err := d.cmd(ctx, "rm", "-f", containerName); err != nil {
		return res, err
	}

	res.SinkRecords, err = d.countRecords(ctx, cfg.sinks, s0, s1)
	if err != nil {
		return res, err
	}
	var total int64
	for i := range cfg.sinks {
		res.SinkBytes = append(res.SinkBytes, s1[i]-s0[i])
		total += res.SinkRecords[i]
	}
	res.Rate = float64(total) / float64(len(cfg.sinks)) / res.WindowSec
	if total == 0 {
		return res, cerrors.Errorf("no records reached the sinks during the window")
	}
	return res, nil
}

// start launches the Conduit container for spec and returns once docker has
// started it. It does not wait for the pipeline.
func (d docker) start(ctx context.Context, cfg config, spec runSpec) (string, error) {
	cfg = cfg.forSpec(spec)
	args := []string{
		"run", "-d", "--name", containerName,
		"-v", volumeName + ":/sink",
		"-v", cfg.pipelineFile + ":/app/pipelines/pipeline.yml:ro",
		"-w", "/app",
	}
	if spec.Binary != "" {
		args = append(args, "-v", spec.Binary+":/app/conduit:ro")
	}
	for _, e := range cfg.Env {
		args = append(args, "-e", e)
	}
	if cfg.CPUs != "" {
		args = append(args, "--cpus", cfg.CPUs)
	}
	args = append(args, d.imageFor(spec), "/app/conduit", "run", "--log.level", "info")
	if spec.Engine == engineV2 {
		args = append(args, "--preview.pipeline-arch-v2")
	}
	return d.cmd(ctx, args...)
}

// skippedArm is an arm the probe could not get output from.
type skippedArm struct {
	Label  string `json:"label"`
	Reason string `json:"reason"`
}

// probeArms starts every arm once and keeps those whose sinks fill within
// cfg.ProbeTimeout. An arm that is refused at start (for example arch-v2 with a
// destination that batches, pipeline.archv2_destination_batching_unsupported)
// would otherwise cost a full sink timeout in every round and abort the
// session. It is dropped, and the reason is recorded in env.json and the
// summary, so a refusal is a reported result and not a missing row.
func (d docker) probeArms(ctx context.Context, cfg config) ([]armSpec, []skippedArm) {
	var ok []armSpec
	var skipped []skippedArm
	for _, a := range cfg.Arms {
		spec := runSpec{Arm: a.Label, Engine: a.Engine, Binary: a.Binary, Shape: a.Shape}
		cfg := cfg.forSpec(spec)
		fmt.Fprintf(os.Stderr, "probe %s (%s) ...\n", a.Label, a.Engine)
		_, _ = d.cmd(ctx, "rm", "-f", containerName)
		_, err := d.start(ctx, cfg, spec)
		if err == nil {
			err = d.waitForSinksWithin(ctx, cfg.sinks, cfg.ProbeTimeout)
		}
		_, _ = d.cmd(ctx, "rm", "-f", containerName)
		_, _ = d.cmd(ctx, "run", "--rm", "-v", volumeName+":/sink", d.baseImage, "sh", "-c", "rm -f /sink/*")
		if err != nil {
			fmt.Fprintf(os.Stderr, "      skipped: %v\n", err)
			skipped = append(skipped, skippedArm{Label: a.Label, Reason: err.Error()})
			continue
		}
		ok = append(ok, a)
	}
	return ok, skipped
}

func (d docker) waitForSinks(ctx context.Context, sinks []string) error {
	return d.waitForSinksWithin(ctx, sinks, sinkReadyTimeout)
}

func (d docker) waitForSinksWithin(ctx context.Context, sinks []string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if err := d.assertRunning(ctx); err != nil {
			return err
		}
		sizes, _, err := d.sinkSizes(ctx, sinks)
		if err == nil && allPositive(sizes) {
			return nil
		}
		if time.Now().After(deadline) {
			return cerrors.Errorf("sinks %v not written within %s; conduit errors:\n%s", sinks, timeout, d.errorLines(ctx))
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
}

// engineLogLine is what Conduit logs at info level when it runs pipelines on
// arch-v2. The classic engine logs nothing like it.
const engineLogLine = "using lifecycle service v2"

// attestEngine checks the container's own log for which engine ran. Without
// it, a mistyped flag or a binary that ignores it would silently turn an A/B
// comparison into an A/A one, and the A/A floor would not show it.
func (d docker) attestEngine(ctx context.Context, spec runSpec) error {
	logs, err := d.cmd(ctx, "logs", containerName)
	if err != nil {
		return cerrors.Errorf("read logs to attest the engine: %w", err)
	}
	return checkEngineLog(spec, logs)
}

// checkEngineLog is attestEngine's decision, separated so it can be tested
// without Docker.
func checkEngineLog(spec runSpec, logs string) error {
	ranV2 := strings.Contains(logs, engineLogLine)
	switch {
	case spec.Engine == engineV2 && !ranV2:
		return cerrors.Errorf("arm %s asked for arch-v2 but the log lacks %q; run is invalid", spec.Arm, engineLogLine)
	case spec.Engine == engineV1 && ranV2:
		return cerrors.Errorf("arm %s asked for the classic engine but the log says %q; run is invalid", spec.Arm, engineLogLine)
	}
	return nil
}

var ansiRe = regexp.MustCompile(`\x1b\[[0-9;]*m`)

// errorLines returns the last few error-level lines of the container's log,
// without colour codes and cut to a readable length. It is what explains an
// arm that never produced output, such as a pipeline refused at start.
func (d docker) errorLines(ctx context.Context) string {
	logs, _ := d.cmd(ctx, "logs", containerName)
	var errs []string
	for _, l := range strings.Split(ansiRe.ReplaceAllString(logs, ""), "\n") {
		if strings.Contains(l, " ERR ") {
			if len(l) > 600 {
				l = l[:600] + "..."
			}
			errs = append(errs, l)
		}
	}
	if len(errs) > 3 {
		errs = errs[len(errs)-3:]
	}
	if len(errs) == 0 {
		return "(no error-level log lines)"
	}
	return strings.Join(errs, "\n")
}

func (d docker) assertRunning(ctx context.Context) error {
	out, err := d.cmd(ctx, "inspect", "-f", "{{.State.Running}}", containerName)
	if err != nil {
		return err
	}
	if strings.TrimSpace(out) != "true" {
		logs, _ := d.cmd(ctx, "logs", "--tail", "30", containerName)
		return cerrors.Errorf("conduit container is not running; last logs:\n%s", logs)
	}
	return nil
}

// sinkSizes returns the byte size of every sink and the time the sizes were
// taken: the midpoint of the docker exec call, whose own latency is a few
// milliseconds against a 60s window.
func (d docker) sinkSizes(ctx context.Context, sinks []string) ([]int64, time.Time, error) {
	before := time.Now()
	out, err := d.cmd(ctx, append([]string{"exec", containerName, "stat", "-c", "%s"}, sinks...)...)
	after := time.Now()
	if err != nil {
		return nil, time.Time{}, err
	}
	sizes, err := parseInts(out, len(sinks))
	if err != nil {
		return nil, time.Time{}, cerrors.Errorf("parse sink sizes: %w", err)
	}
	return sizes, before.Add(after.Sub(before) / 2), nil
}

// countRecords counts the newline-terminated records each sink gained in the
// byte range [s0, s1), then empties the sink volume for the next run. It runs
// in a throwaway container on the same image, which ships busybox.
func (d docker) countRecords(ctx context.Context, sinks []string, s0, s1 []int64) ([]int64, error) {
	var script strings.Builder
	for i, p := range sinks {
		// tail -c +N starts at byte N (1-based), so +s0+1 is offset s0.
		fmt.Fprintf(&script, "tail -c +%d %s | head -c %d | wc -l; ", s0[i]+1, p, s1[i]-s0[i])
	}
	script.WriteString("rm -f /sink/*")
	out, err := d.cmd(ctx, "run", "--rm", "-v", volumeName+":/sink", "--entrypoint", "sh", d.baseImage, "-c", script.String())
	if err != nil {
		return nil, err
	}
	return parseInts(out, len(sinks))
}

func (d docker) vmLoad(ctx context.Context) string {
	out, err := d.cmd(ctx, "exec", containerName, "cat", "/proc/loadavg")
	if err != nil {
		return unknown
	}
	f := strings.Fields(out)
	if len(f) < 3 {
		return unknown
	}
	return strings.Join(f[:3], " ")
}

func parseInts(out string, want int) ([]int64, error) {
	fields := strings.Fields(out)
	if len(fields) != want {
		return nil, cerrors.Errorf("want %d numbers, got %q", want, out)
	}
	vals := make([]int64, want)
	for i, f := range fields {
		v, err := strconv.ParseInt(f, 10, 64)
		if err != nil {
			return nil, cerrors.Errorf("parse %q: %w", f, err)
		}
		vals[i] = v
	}
	return vals, nil
}

func allPositive(xs []int64) bool {
	for _, x := range xs {
		if x <= 0 {
			return false
		}
	}
	return len(xs) > 0
}

func sleep(ctx context.Context, d time.Duration) error {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}

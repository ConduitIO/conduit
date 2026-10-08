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
	"os/exec"
	"strconv"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
)

const (
	containerName = "archv2-gate-conduit"
	volumeName    = "archv2-gate-sink"
	// sinkReadyTimeout bounds how long a run waits for every sink file to
	// exist and hold at least one record before giving up.
	sinkReadyTimeout = 90 * time.Second
)

type docker struct {
	image string
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

// preflight checks the image exists and starts from a clean container and
// sink volume.
func (d docker) preflight(ctx context.Context) error {
	if _, err := d.cmd(ctx, "image", "inspect", d.image); err != nil {
		return cerrors.Errorf("image %q not found; build it first (see README.md): %w", d.image, err)
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
	res := runResult{runSpec: spec, Started: time.Now().UTC(), HostLoad: hostLoad(ctx)}

	_, _ = d.cmd(ctx, "rm", "-f", containerName)
	args := []string{
		"run", "-d", "--name", containerName,
		"-v", volumeName + ":/sink",
		"-v", cfg.pipelineFile + ":/app/pipelines/pipeline.yml:ro",
	}
	if cfg.CPUs != "" {
		args = append(args, "--cpus", cfg.CPUs)
	}
	args = append(args, d.image, "/app/conduit", "run", "--log.level", "error")
	if spec.Engine == engineV2 {
		args = append(args, "--preview.pipeline-arch-v2")
	}
	if _, err := d.cmd(ctx, args...); err != nil {
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

func (d docker) waitForSinks(ctx context.Context, sinks []string) error {
	deadline := time.Now().Add(sinkReadyTimeout)
	for {
		if err := d.assertRunning(ctx); err != nil {
			return err
		}
		sizes, _, err := d.sinkSizes(ctx, sinks)
		if err == nil && allPositive(sizes) {
			return nil
		}
		if time.Now().After(deadline) {
			logs, _ := d.cmd(ctx, "logs", "--tail", "30", containerName)
			return cerrors.Errorf("sinks %v not written within %s; last conduit logs:\n%s", sinks, sinkReadyTimeout, logs)
		}
		if err := sleep(ctx, 500*time.Millisecond); err != nil {
			return err
		}
	}
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
	out, err := d.cmd(ctx, "run", "--rm", "-v", volumeName+":/sink", "--entrypoint", "sh", d.image, "-c", script.String())
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

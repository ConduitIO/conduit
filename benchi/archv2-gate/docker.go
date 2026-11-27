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
	// sinkMount is where the sink volume is mounted in the container. Every
	// shape's pipeline.yml writes its sinks under this directory.
	sinkMount = "/sink"
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

// combined runs a command and returns stdout and stderr together, in the
// order they were written.
func combined(ctx context.Context, name string, args ...string) (string, error) {
	c := exec.CommandContext(ctx, name, args...)
	var both bytes.Buffer
	c.Stdout, c.Stderr = &both, &both
	err := c.Run()
	return both.String(), err
}

// containerLogs returns the tail of the container's output. It must read
// stdout AND stderr: Conduit writes its startup errors to stderr, and reading
// only stdout showed the ASCII banner and nothing else, which is how a
// failing gate run on 2026-11-27 reported no cause at all.
func (d docker) containerLogs(ctx context.Context) string {
	out, _ := combined(ctx, "docker", "logs", "--tail", "30", containerName)
	return out
}

// runArgs builds the `docker run` command line for one run. Extracted so the
// flags both arms get, and the single flag they differ by, are testable
// without Docker.
func runArgs(image string, cfg config, spec runSpec) []string {
	args := []string{
		"run", "-d", "--name", containerName,
		"-v", volumeName + ":" + sinkMount,
		"-v", cfg.pipelineFile + ":/app/pipelines/pipeline.yml:ro",
	}
	if cfg.CPUs != "" {
		args = append(args, "--cpus", cfg.CPUs)
	}
	args = append(args, image, "/app/conduit", "run", "--log.level", "error",
		// The image sets CONDUIT_API_HTTP_ADDRESS and
		// CONDUIT_API_GRPC_ADDRESS to 0.0.0.0 so a published port is
		// reachable. Conduit refuses to start on a non-loopback bind with no
		// API token (`code: api.token_required`), and the harness never talks
		// to the API, so these two flags put the addresses back to the
		// binary's own defaults. Without them every run of every arm exits
		// during startup.
		"--api.http.address", "127.0.0.1:8080",
		"--api.grpc.address", "127.0.0.1:8084",
		// builtin:file may only write inside --connectors.file.allowed-dirs,
		// which defaults to /app/files. The sink volume is mounted at /sink,
		// so without this the destination connector refuses every path with
		// "the path is outside every allowed directory" and no records reach
		// a sink. Both arms get it; it widens where the file connector may
		// write and changes nothing about the engine or the pipeline.
		"--connectors.file.allowed-dirs", sinkMount)
	if spec.Engine == engineV2 {
		args = append(args, "--preview.pipeline-arch-v2")
	}
	return args
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
	return d.giveSinkVolumeToTheImageUser(ctx)
}

// giveSinkVolumeToTheImageUser makes the sink volume writable by the user the
// image runs as. Docker creates a named volume whose mount point does not
// exist in the image as root-owned and mode 0755, and the Conduit image runs
// as uid 10001, so the file destination cannot create its sink file at all:
// "open /sink/sink-1.jsonl: permission denied", on every arm of every round.
// The chown runs in a throwaway container from the same image, so it needs no
// other image and no root on the host. Running Conduit itself as root would
// also work and is not done: the measured process should be the one the image
// ships.
func (d docker) giveSinkVolumeToTheImageUser(ctx context.Context) error {
	user, err := d.cmd(ctx, "image", "inspect", "-f", "{{.Config.User}}", d.image)
	if err != nil {
		return err
	}
	u := strings.TrimSpace(user)
	if u == "" || u == "0" || u == "0:0" || u == "root" || u == "root:root" {
		return nil // already runs as root; the volume is root-owned
	}
	if _, err := d.cmd(ctx, "run", "--rm", "-u", "0:0",
		"-v", volumeName+":"+sinkMount, "--entrypoint", "chown", d.image, u, sinkMount); err != nil {
		return cerrors.Errorf("could not give %s in the sink volume to %s: %w", sinkMount, u, err)
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
	if _, err := d.cmd(ctx, runArgs(d.image, cfg, spec)...); err != nil {
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
			return cerrors.Errorf("sinks %v not written within %s; last conduit logs:\n%s",
				sinks, sinkReadyTimeout, d.containerLogs(ctx))
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
		return cerrors.Errorf("conduit container is not running; last logs:\n%s", d.containerLogs(ctx))
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

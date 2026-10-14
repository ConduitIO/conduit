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
	"encoding/csv"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"time"

	"github.com/conduitio/conduit/pkg/foundation/cerrors"
	"github.com/goccy/go-json"
)

// environment is written to env.json at the start of a session and updated at
// the end. It is the machine-readable half of the environment spec: what the
// numbers were measured on.
type environment struct {
	Started        time.Time `json:"started"`
	Finished       time.Time `json:"finished,omitzero"`
	Image          string    `json:"image"`
	Shape          string    `json:"shape"`
	Session        string    `json:"session"`
	Rounds         int       `json:"rounds"`
	Warmup         string    `json:"warmup"`
	Window         string    `json:"window"`
	CPUs           string    `json:"cpus,omitempty"`
	Sinks          []string  `json:"sinks"`
	HostOS         string    `json:"host_os"`
	HostArch       string    `json:"host_arch"`
	HostCPU        string    `json:"host_cpu"`
	HostCPUs       int       `json:"host_cpus"`
	HostMemBytes   string    `json:"host_mem_bytes"`
	HostLoadStart  string    `json:"host_load_start"`
	DockerVersion  string    `json:"docker_server_version"`
	DockerOS       string    `json:"docker_os"`
	DockerKernel   string    `json:"docker_kernel"`
	DockerCPUs     string    `json:"docker_cpus"`
	DockerMemBytes string    `json:"docker_mem_bytes"`
	ImageID        string    `json:"image_id"`
	SourceRevision string    `json:"source_revision"`
}

func captureEnvironment(ctx context.Context, cfg config, d docker) environment {
	env := environment{
		Started:       time.Now().UTC(),
		Image:         cfg.Image,
		Shape:         cfg.Shape,
		Session:       string(cfg.Session),
		Rounds:        cfg.Rounds,
		Warmup:        cfg.Warmup.String(),
		Window:        cfg.Window.String(),
		CPUs:          cfg.CPUs,
		Sinks:         cfg.sinks,
		HostOS:        runtime.GOOS,
		HostArch:      runtime.GOARCH,
		HostCPUs:      runtime.NumCPU(),
		HostLoadStart: hostLoad(ctx),
	}
	switch runtime.GOOS {
	case "darwin":
		env.HostCPU = output(ctx, "sysctl", "-n", "machdep.cpu.brand_string")
		env.HostMemBytes = output(ctx, "sysctl", "-n", "hw.memsize")
	case "linux":
		env.HostCPU = grepFirst("/proc/cpuinfo", "model name")
		env.HostMemBytes = grepFirst("/proc/meminfo", "MemTotal")
	}
	info, _ := d.cmd(ctx, "info", "--format", "{{.ServerVersion}}|{{.OperatingSystem}}|{{.KernelVersion}}|{{.NCPU}}|{{.MemTotal}}")
	if f := strings.Split(strings.TrimSpace(info), "|"); len(f) == 5 {
		env.DockerVersion, env.DockerOS, env.DockerKernel, env.DockerCPUs, env.DockerMemBytes = f[0], f[1], f[2], f[3], f[4]
	}
	id, _ := d.cmd(ctx, "image", "inspect", "--format", "{{.Id}}", d.image)
	env.ImageID = strings.TrimSpace(id)
	// The Dockerfile does not stamp a version into the binary, so the source
	// revision comes from the label the README's build command sets.
	rev, _ := d.cmd(ctx, "image", "inspect", "--format", `{{index .Config.Labels "org.opencontainers.image.revision"}}`, d.image)
	env.SourceRevision = strings.TrimSpace(rev)
	if env.SourceRevision == "" || env.SourceRevision == "<no value>" {
		env.SourceRevision = "unknown (image built without the revision label)"
	}
	return env
}

// hostLoad returns the host's 1/5/15-minute load averages. Under Docker
// Desktop the container runs in a VM, so runs.csv also records the VM's load.
func hostLoad(ctx context.Context) string {
	switch runtime.GOOS {
	case "darwin":
		return strings.Trim(output(ctx, "sysctl", "-n", "vm.loadavg"), "{} ")
	case "linux":
		raw, err := os.ReadFile("/proc/loadavg")
		if err != nil {
			return unknown
		}
		f := strings.Fields(string(raw))
		if len(f) >= 3 {
			return strings.Join(f[:3], " ")
		}
	}
	return unknown
}

func output(ctx context.Context, name string, args ...string) string {
	out, err := exec.CommandContext(ctx, name, args...).Output()
	if err != nil {
		return unknown
	}
	return strings.TrimSpace(string(out))
}

func grepFirst(path, prefix string) string {
	raw, err := os.ReadFile(path)
	if err != nil {
		return unknown
	}
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, prefix) {
			if _, v, ok := strings.Cut(line, ":"); ok {
				return strings.TrimSpace(v)
			}
		}
	}
	return unknown
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return cerrors.Errorf("encode %s: %w", path, err)
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}

func writeRunsCSV(path string, results []runResult) error {
	f, err := os.Create(path)
	if err != nil {
		return cerrors.Errorf("create %s: %w", path, err)
	}
	w := csv.NewWriter(f)
	_ = w.Write([]string{
		"round", "arm", "engine", "started_utc", "window_s",
		"sink_records", "sink_bytes", "records_per_s_per_sink",
		"host_load", "vm_load_before", "vm_load_after",
	})
	for _, r := range results {
		_ = w.Write([]string{
			strconv.Itoa(r.Round), r.Arm, string(r.Engine), r.Started.Format(time.RFC3339),
			strconv.FormatFloat(r.WindowSec, 'f', 3, 64),
			joinInts(r.SinkRecords), joinInts(r.SinkBytes),
			strconv.FormatFloat(r.Rate, 'f', 1, 64),
			r.HostLoad, r.VMLoadBefore, r.VMLoadAfter,
		})
	}
	w.Flush()
	if err := w.Error(); err != nil {
		_ = f.Close()
		return cerrors.Errorf("write %s: %w", path, err)
	}
	return f.Close()
}

func joinInts(xs []int64) string {
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = strconv.FormatInt(x, 10)
	}
	return strings.Join(s, " ")
}

// renderSummary turns a session's runs into summary.md. For an A/A session it
// reports the floor; for an A/B session it reports the A/B result and, beside
// it, the floor from the same session's v1 pair.
func renderSummary(cfg config, results []runResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# archv2-gate: shape %s, session %s\n\n", cfg.Shape, cfg.Session)
	fmt.Fprintf(&b, "%d rounds, %s warmup discarded, %s window, records counted at the sink.\n"+
		"Environment in env.json, every run in runs.csv.\n\n", cfg.Rounds, cfg.Warmup, cfg.Window)

	byArm := map[string][]float64{}
	var arms []string
	for _, r := range results {
		if _, ok := byArm[r.Arm]; !ok {
			arms = append(arms, r.Arm)
		}
		byArm[r.Arm] = append(byArm[r.Arm], r.Rate)
	}

	b.WriteString("| arm | n | median rec/s | sd % | min | max |\n| --- | --- | --- | --- | --- | --- |\n")
	for _, arm := range arms {
		s := summarize(byArm[arm])
		fmt.Fprintf(&b, "| %s | %d | %.0f | %.1f | %.0f | %.0f |\n", arm, s.N, s.Median, s.SDPct, s.Min, s.Max)
	}
	b.WriteString("\n")

	aArm, bArm := armV1A, armV1B
	if cfg.Session == sessionAAv2 {
		aArm, bArm = "v2-a", "v2-b"
	}
	aa := pairedDeltas(results, aArm, bArm, nil)
	aaMedian := deltaPct(summarize(byArm[aArm]).Median, summarize(byArm[bArm]).Median)
	fmt.Fprintf(&b, "## A/A control (%s vs %s)\n\n", bArm, aArm)
	fmt.Fprintf(&b, "- per-round deltas: %s\n", fmtPcts(aa))
	fmt.Fprintf(&b, "- median-to-median delta: %+.1f%%\n", aaMedian)
	fmt.Fprintf(&b, "- **A/A floor (largest per-round |delta|): ±%.1f%%**\n\n", maxAbs(aa))

	if cfg.Session == sessionAB {
		v1 := append(append([]float64(nil), byArm[armV1A]...), byArm[armV1B]...)
		abMedian := deltaPct(summarize(v1).Median, summarize(byArm[armV2]).Median)
		ab := pairedDeltas(results, "", armV2, []string{armV1A, armV1B})
		b.WriteString("## A/B (v2 vs v1)\n\n")
		fmt.Fprintf(&b, "- per-round deltas, v2 against the mean of the v1 runs bracketing it: %s\n", fmtPcts(ab))
		fmt.Fprintf(&b, "- median-to-median delta, v2 vs all v1 runs: %+.1f%%\n", abMedian)
		fmt.Fprintf(&b, "- A/A floor from this session, quoted beside it: ±%.1f%%\n\n", maxAbs(aa))
	}

	// Two lines, each under markdownlint's 120-character limit.
	b.WriteString("Read this against the A/A floor, not on its own. A difference inside the floor is not\n" +
		"a difference this session can resolve. Rates from different sessions are not comparable.\n")
	return b.String()
}

// pairedDeltas returns, per round, the percentage delta of arm b against a
// baseline: arm a's run, or, when bracket is set, the mean of the bracket
// arms' runs in that round.
func pairedDeltas(results []runResult, a, b string, bracket []string) []float64 {
	byRound := map[int]map[string]float64{}
	var rounds []int
	for _, r := range results {
		if _, ok := byRound[r.Round]; !ok {
			byRound[r.Round] = map[string]float64{}
			rounds = append(rounds, r.Round)
		}
		byRound[r.Round][r.Arm] = r.Rate
	}

	var out []float64
	for _, round := range rounds {
		arms := byRound[round]
		bv, ok := arms[b]
		if !ok {
			continue
		}
		var base float64
		if bracket == nil {
			av, ok := arms[a]
			if !ok {
				continue
			}
			base = av
		} else {
			n := 0
			for _, arm := range bracket {
				if v, ok := arms[arm]; ok {
					base += v
					n++
				}
			}
			if n != len(bracket) {
				continue
			}
			base /= float64(n)
		}
		out = append(out, deltaPct(base, bv))
	}
	return out
}

func fmtPcts(xs []float64) string {
	if len(xs) == 0 {
		return "none (incomplete rounds)"
	}
	s := make([]string, len(xs))
	for i, x := range xs {
		s[i] = fmt.Sprintf("%+.1f%%", x)
	}
	return strings.Join(s, ", ")
}

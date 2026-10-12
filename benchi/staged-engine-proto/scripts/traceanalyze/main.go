// traceanalyze: per-role goroutine state time from a Go execution trace.
package main

import (
	"fmt"
	"io"
	"os"
	"sort"
	"strings"
	"time"

	"golang.org/x/exp/trace"
)

type gs struct {
	role    string
	state   trace.GoState
	reason  string
	since   trace.Time
	acc     map[string]time.Duration // state/reason -> dur
	started bool
}

var createCount = map[string]int{}

var roles = []struct{ frag, name string }{
	{"funnel.(*Worker).readLoop", "proto.reader"},
	{"funnel.(*Worker).doPipelined", "proto.runner(Do)"},
	{"funnel.(*Worker).coordinate", "proto.coordinator"},
	{"funnel.(*DestinationTask).writer", "proto.dest.writer"},
	{"funnel.(*DestinationTask).ackReader", "proto.dest.ackReader"},
	{"connector.(*benchLatency).recv", "bench.ackPump"},
	{"ConnectorMetricsImpl.Observe.func1", "engine.metricsObserveGoroutine"},
	{"funnel.(*Worker).Do", "engine.workerLoop"},
	{"funnel.(*Worker).Do", "engine.workerLoop"},
	{"sdk.(*sourcePluginAdapter).runRead", "plugin.source.runRead"},
	{"sdk.(*sourcePluginAdapter).runAck", "plugin.source.runAck"},
	{"sdk.(*destinationPluginAdapter).Run", "plugin.dest.Run"},
	{"connector.(*Source).deliverDeferredAcks", "engine.source.deliverDeferredAcks"},
	{"connector.(*Persister)", "engine.persister"},
	{"connector.(*Destination)", "engine.destination"},
	{"generator", "plugin.generator"},
	{"file.", "plugin.file"},
	{"stream.", "v1.stream"},
	{"SourceNode", "v1.SourceNode"},
	{"SourceAckerNode", "v1.SourceAckerNode"},
	{"DestinationNode", "v1.DestinationNode"},
	{"DestinationAckerNode", "v1.DestinationAckerNode"},
	{"FaninNode", "v1.FaninNode"},
	{"FanoutNode", "v1.FanoutNode"},
	{"MetricsNode", "v1.MetricsNode"},
	{"SourceAckerNode", "v1.SourceAckerNode"},
}

func roleOf(st trace.Stack) string {
	var frames []string
	st.Frames()(func(f trace.StackFrame) bool { frames = append(frames, f.Func); return true })
	join := strings.Join(frames, "|")
	// prefer the more specific names: check v1 node names first, then plugin, then engine
	for _, k := range []string{
		"DestinationAckerNode", "SourceAckerNode", "DestinationNode", "SourceNode", "FaninNode", "FanoutNode", "MetricsNode", "ProcessorNode", "DLQHandlerNode", "ackerNode",
	} {
		if strings.Contains(join, k) {
			return "v1." + k
		}
	}
	for _, r := range roles {
		if strings.Contains(join, r.frag) {
			return r.name
		}
	}
	return ""
}

func main() {
	f, _ := os.Open(os.Args[1])
	defer f.Close()
	r, err := trace.NewReader(f)
	if err != nil {
		panic(err)
	}
	gor := map[trace.GoID]*gs{}
	var first, last trace.Time
	for {
		ev, err := r.ReadEvent()
		if err == io.EOF {
			break
		}
		if err != nil {
			panic(err)
		}
		if first == 0 {
			first = ev.Time()
		}
		last = ev.Time()
		if ev.Kind() != trace.EventStateTransition {
			continue
		}
		st := ev.StateTransition()
		if st.Resource.Kind != trace.ResourceGoroutine {
			continue
		}
		id := st.Resource.Goroutine()
		from, to := st.Goroutine()
		if from == trace.GoNotExist && to != trace.GoNotExist {
			// goroutine creation: record the creating stack top frames
			var fr []string
			if ev.Stack() != trace.NoStack {
				ev.Stack().Frames()(func(f trace.StackFrame) bool {
					if len(fr) < 4 {
						fr = append(fr, f.Func)
					}
					return true
				})
			}
			createCount[strings.Join(fr, " < ")]++
		}
		g := gor[id]
		if g == nil {
			g = &gs{acc: map[string]time.Duration{}}
			gor[id] = g
		}
		if g.started {
			key := g.state.String()
			if g.state == trace.GoWaiting && g.reason != "" {
				key = "waiting:" + g.reason
			}
			g.acc[key] += ev.Time().Sub(g.since)
		}
		_ = from
		g.state = to
		g.reason = st.Reason
		g.since = ev.Time()
		g.started = true
		if from == trace.GoRunning && g.role == "" {
			if st.Stack != trace.NoStack {
				g.role = roleOf(st.Stack)
			}
			if g.role == "" {
				if s := ev.Stack(); s != trace.NoStack {
					g.role = roleOf(s)
				}
			}
		}
	}
	span := last.Sub(first)
	{
		type kv struct {
			k string
			v int
		}
		var l []kv
		for k, v := range createCount {
			l = append(l, kv{k, v})
		}
		sort.Slice(l, func(i, j int) bool { return l[i].v > l[j].v })
		fmt.Println("goroutine creations (creator stack top 4):")
		for i, e := range l {
			if i > 8 {
				break
			}
			fmt.Printf("  %8d  %s\n", e.v, e.k)
		}
	}
	fmt.Printf("trace span: %v\n", span.Round(time.Millisecond))
	// aggregate per role
	type agg struct {
		n   int
		acc map[string]time.Duration
	}
	roleAgg := map[string]*agg{}
	for _, g := range gor {
		if g.started {
			key := g.state.String()
			if g.state == trace.GoWaiting && g.reason != "" {
				key = "waiting:" + g.reason
			}
			g.acc[key] += last.Sub(g.since)
		}
		role := g.role
		if role == "" {
			role = "(other)"
		}
		a := roleAgg[role]
		if a == nil {
			a = &agg{acc: map[string]time.Duration{}}
			roleAgg[role] = a
		}
		a.n++
		for k, v := range g.acc {
			a.acc[k] += v
		}
	}
	var names []string
	for k := range roleAgg {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, n := range names {
		a := roleAgg[n]
		if n == "(other)" {
			continue
		}
		var tot time.Duration
		for _, v := range a.acc {
			tot += v
		}
		fmt.Printf("\n%s  (goroutines=%d, observed=%v)\n", n, a.n, tot.Round(time.Millisecond))
		var ks []string
		for k := range a.acc {
			ks = append(ks, k)
		}
		sort.Slice(ks, func(i, j int) bool { return a.acc[ks[i]] > a.acc[ks[j]] })
		for _, k := range ks {
			if a.acc[k] < tot/200 {
				continue
			}
			fmt.Printf("   %-40s %6.1f%%  %v\n", k, 100*float64(a.acc[k])/float64(tot), a.acc[k].Round(time.Microsecond))
		}
	}
}

package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"io"
	"log"
	"net/http"
	"os"
	"os/signal"
	"strconv"
	"sync"
	"syscall"
	"time"
)

// receiver is the ground truth of the HTTP-sink cells: it answers every POST
// with 200 once the body is in its buffered dump file (so a sink that waits for
// the response is acknowledged only after the receiver has the event), counts
// events, and measures latency from each payload marker to arrival.
//
// A body that is a JSON array is a batch (Debezium Server's http sink with
// batching on); each element is one event. Anything else is one event.
//
// -delay makes every response wait, to put a slow destination behind the
// sink (backpressure cells). The receiver runs on the infrastructure cores.
type receiver struct {
	mu      sync.Mutex
	out     *bufio.Writer
	count   int64
	bytes   int64
	secs    secHists // per arrival second: count, bytes, exact latency histogram
	delay   time.Duration
	dumpErr error
}

func (rc *receiver) handle(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost && r.Method != http.MethodPut {
		w.WriteHeader(http.StatusOK) // HEAD/GET connection checks
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	now := time.Now()
	events := splitEvents(body)
	if rc.delay > 0 {
		time.Sleep(rc.delay)
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	for _, ev := range events {
		ev = bytes.ReplaceAll(ev, []byte{'\n'}, []byte{' '})
		if _, err := rc.out.Write(append(ev, '\n')); err != nil && rc.dumpErr == nil {
			rc.dumpErr = err
		}
		rc.count++
		rc.bytes += int64(len(ev))
		var ts int64
		if ms := findMarkers(string(ev)); len(ms) > 0 {
			ts = ms[len(ms)-1].TS // the after image when a before image is present
		}
		rc.secs.add(now.UnixMilli(), len(ev), ts)
	}
	if rc.dumpErr != nil {
		http.Error(w, rc.dumpErr.Error(), http.StatusInternalServerError)
		return
	}
	w.WriteHeader(http.StatusOK)
}

func splitEvents(body []byte) [][]byte {
	t := bytes.TrimSpace(body)
	if len(t) == 0 || t[0] != '[' {
		return [][]byte{t}
	}
	var arr []json.RawMessage
	if err := json.Unmarshal(t, &arr); err != nil {
		return [][]byte{t}
	}
	out := make([][]byte, len(arr))
	for i, a := range arr {
		out[i] = a
	}
	return out
}

func (rc *receiver) stats(w http.ResponseWriter, _ *http.Request) {
	rc.mu.Lock()
	c, b := rc.count, rc.bytes
	rc.mu.Unlock()
	_ = json.NewEncoder(w).Encode(map[string]any{"ts_ms": time.Now().UnixMilli(), "count": c, "bytes": b})
}

// hist returns every second's histogram (collect http-hist, after the run).
func (rc *receiver) hist(w http.ResponseWriter, _ *http.Request) {
	rc.mu.Lock()
	defer rc.mu.Unlock()
	_ = rc.out.Flush()
	out := map[string]*secHist{}
	for k, v := range rc.secs {
		out[strconv.FormatInt(k, 10)] = v
	}
	_ = json.NewEncoder(w).Encode(map[string]any{"secs": out})
}

func runReceive(args []string) error {
	fs := flag.NewFlagSet("receive", flag.ExitOnError)
	addr := fs.String("addr", ":8090", "listen address")
	dump := fs.String("dump", "/sink/http.jsonl", "file every received event is appended to, one per line")
	delay := fs.Duration("delay", 0, "wait this long before answering each request (slow-destination cells)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	f, err := os.OpenFile(*dump, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o666)
	if err != nil {
		return err
	}
	defer f.Close()
	rc := &receiver{out: bufio.NewWriterSize(f, 1<<20), secs: secHists{}, delay: *delay}
	go func() {
		for range time.Tick(time.Second) {
			rc.mu.Lock()
			_ = rc.out.Flush()
			rc.mu.Unlock()
		}
	}()
	mux := http.NewServeMux()
	mux.HandleFunc("/stats", rc.stats)
	mux.HandleFunc("/hist", rc.hist)
	mux.HandleFunc("/", rc.handle)
	srv := &http.Server{Addr: *addr, Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	go func() {
		<-ctx.Done()
		_ = srv.Shutdown(context.Background())
	}()
	log.Printf("receive: listening on %s, dumping to %s", *addr, *dump)
	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	rc.mu.Lock()
	defer rc.mu.Unlock()
	return rc.out.Flush()
}

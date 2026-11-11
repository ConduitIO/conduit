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

// Command registry is the schema registry the SMT oracle runs Conduit against:
// the same in-memory Confluent-compatible server Conduit's tests use, with a
// dump on shutdown. capture.sh points a real Conduit Postgres source at it, so
// every schema behind a captured OpenCDC record is registered here, and writes
// the result to fixtures/conduit-schemas.json.
//
// It prints its base URL on the first line of stdout and, on SIGTERM or
// SIGINT, writes every subject version to -dump and exits.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"sort"
	"syscall"
	"time"

	"github.com/goccy/go-json"

	"github.com/conduitio/conduit-commons/database/inmemory"
	schemaregistry "github.com/conduitio/conduit-schema-registry"
	"github.com/twmb/franz-go/pkg/sr"
)

// Entry is one registered schema, in the format of fixtures/conduit-schemas.json.
type Entry struct {
	Subject string `json:"subject"`
	Version int    `json:"version"`
	Type    string `json:"type"`
	Schema  string `json:"schema"`
}

func main() {
	dump := flag.String("dump", "", "file to write the registered schemas to on shutdown")
	flag.Parse()
	if err := run(*dump); err != nil {
		fmt.Fprintln(os.Stderr, "registry:", err)
		os.Exit(1)
	}
}

func run(dump string) error {
	reg, err := schemaregistry.NewSchemaRegistry(&inmemory.DB{})
	if err != nil {
		return err
	}
	mux := http.NewServeMux()
	schemaregistry.NewServer(slog.New(slog.NewTextHandler(io.Discard, nil)), reg).RegisterHandlers(mux)
	l, err := (&net.ListenConfig{}).Listen(context.Background(), "tcp", "127.0.0.1:0")
	if err != nil {
		return err
	}
	srv := &http.Server{Handler: mux, ReadHeaderTimeout: 10 * time.Second}
	go func() { _ = srv.Serve(l) }()
	base := "http://" + l.Addr().String()
	fmt.Println(base)

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig

	entries, err := collect(context.Background(), reg)
	if err != nil {
		return err
	}
	_ = srv.Shutdown(context.Background())
	b, err := json.MarshalIndent(entries, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(dump, append(b, '\n'), 0o600)
}

// collect lists every registered schema through the registry itself: the HTTP
// server does not implement listing subjects, but IDs are sequential from 1.
// The schema text comes from SchemaByID, because SubjectVersionsByID returns
// subject schemas without it.
func collect(ctx context.Context, reg *schemaregistry.SchemaRegistry) ([]Entry, error) {
	entries := []Entry{}
	for id := 1; ; id++ {
		sch, err := reg.SchemaByID(ctx, id)
		if err != nil {
			break // the first unknown ID ends the list
		}
		svs, err := reg.SubjectVersionsByID(ctx, id)
		if err != nil {
			return nil, err
		}
		for _, sv := range svs {
			typ := "AVRO"
			switch sv.Type {
			case sr.TypeAvro:
			case sr.TypeProtobuf:
				typ = "PROTOBUF"
			case sr.TypeJSON:
				typ = "JSON"
			}
			entries = append(entries, Entry{Subject: sv.Subject, Version: sv.Version, Type: typ, Schema: sch.Schema})
		}
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].Subject != entries[j].Subject {
			return entries[i].Subject < entries[j].Subject
		}
		return entries[i].Version < entries[j].Version
	})
	return entries, nil
}

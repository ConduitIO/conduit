package cluster

import (
	"context"
	"io"
	"maps"

	"github.com/conduitio/conduit/pkg/foundation/log"
	"github.com/goccy/go-json"
	"github.com/hashicorp/raft"
)

type FSM struct {
	logger    log.CtxLogger
	positions map[string]string
}

func NewFSM(logger log.CtxLogger) *FSM {
	return &FSM{
		logger:    logger.WithComponent("cluster.FSM"),
		positions: make(map[string]string),
	}
}

// Apply is called once a log entry is committed by a majority of the cluster.
//
// Apply should apply the log to the FSM. Apply must be deterministic and
// produce the same result on all peers in the cluster.
//
// The returned value is returned to the client as the ApplyFuture.Response.
func (f *FSM) Apply(l *raft.Log) interface{} {
	if l.Type != raft.LogCommand {
		return nil
	}

	positions, err := DecodePositions(l.Data)
	if err != nil {
		panic(err)
	}

	for k, v := range positions {
		f.logger.Info(context.Background()).Str(log.ConnectorIDField, k).Str(log.RecordPositionField, v).Msg("applying position")
		f.positions[k] = v
	}

	return nil
}

// Snapshot returns an FSMSnapshot used to: support log compaction, to
// restore the FSM to a previous state, or to bring out-of-date followers up
// to a recent log index.
//
// The Snapshot implementation should return quickly, because Apply can not
// be called while Snapshot is running. Generally this means Snapshot should
// only capture a pointer to the state, and any expensive IO should happen
// as part of FSMSnapshot.Persist.
//
// Apply and Snapshot are always called from the same thread, but Apply will
// be called concurrently with FSMSnapshot.Persist. This means the FSM should
// be implemented to allow for concurrent updates while a snapshot is happening.
func (f *FSM) Snapshot() (raft.FSMSnapshot, error) {
	return snapshot{positions: maps.Clone(f.positions)}, nil
}

// Restore is used to restore an FSM from a snapshot. It is not called
// concurrently with any other command. The FSM must discard all previous
// state before restoring the snapshot.
func (f *FSM) Restore(read io.ReadCloser) error {
	defer read.Close()
	b, err := io.ReadAll(read)
	if err != nil {
		return err
	}

	positions, err := DecodePositions(b)
	if err != nil {
		return err
	}
	f.positions = positions
	return nil
}

var _ raft.FSM = &FSM{}

type snapshot struct {
	positions map[string]string
}

// Persist should dump all necessary state to the WriteCloser 'sink',
// and call sink.Close() when finished or call sink.Cancel() on error.
func (s snapshot) Persist(sink raft.SnapshotSink) error {
	_, err := sink.Write(EncodePositions(s.positions))
	return err
}

// Release is invoked when we are finished with the snapshot.
func (s snapshot) Release() {}

func EncodePositions(p map[string]string) []byte {
	b, err := json.Marshal(p)
	if err != nil {
		panic(err)
	}
	return b
}

func DecodePositions(b []byte) (map[string]string, error) {
	p := make(map[string]string)
	err := json.Unmarshal(b, &p)
	if err != nil {
		return nil, err
	}
	return p, nil
}

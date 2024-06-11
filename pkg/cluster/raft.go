package cluster

import (
	"errors"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/hashicorp/raft"
	raftboltdb "github.com/hashicorp/raft-boltdb/v2"
)

const (
	raftLogCacheSize  = 512
	snapshotsRetained = 2
)

type Raft struct {
	*raft.Raft

	transport *raft.NetworkTransport
	store     *raftboltdb.BoltStore
}

func CreateRaft(
	id string,
	host string,
	port int,
	dataPath string,
	fsm raft.FSM,
) (r *Raft, err error) {
	logger := slog.Default()

	addr := fmt.Sprintf("%s:%d", host, port)

	transportLogger := slog.NewLogLogger(logger.With("component", "raft/tcp-transport").Handler(), slog.LevelInfo)
	transport, err := raft.NewTCPTransport(addr, nil, 3, 10*time.Second, transportLogger.Writer())
	if err != nil {
		return nil, fmt.Errorf("failed to create transport: %w", err)
	}
	defer func() { err = onlyIfError(err, transport.Close) }()

	path := filepath.Join(dataPath, "raft")
	if err := os.MkdirAll(path, 0755); err != nil {
		return nil, fmt.Errorf("failed to create raft directory: %w", err)
	}

	store, err := raftboltdb.NewBoltStore(filepath.Join(path, "raft.db"))
	if err != nil {
		return nil, fmt.Errorf("failed to create bolt store: %w", err)
	}
	defer func() { err = onlyIfError(err, store.Close) }()

	logCache, err := raft.NewLogCache(raftLogCacheSize, store)
	if err != nil {
		return nil, fmt.Errorf("failed to create log cache: %w", err)
	}

	snapshotStoreLogger := slog.NewLogLogger(logger.With("component", "raft/file-snapshot-store").Handler(), slog.LevelInfo)
	snapshotStore, err := raft.NewFileSnapshotStore(path, snapshotsRetained, snapshotStoreLogger.Writer())
	if err != nil {
		return nil, fmt.Errorf("failed to create snapshot store: %w", err)
	}

	raftConfig := raft.DefaultConfig()
	raftConfig.LocalID = raft.ServerID(id)
	raftConfig.SnapshotThreshold = 10
	raftConfig.SnapshotInterval = 2 * time.Second

	hasState, err := raft.HasExistingState(logCache, store, snapshotStore)
	if err != nil {
		return nil, fmt.Errorf("failed to check existing state: %w", err)
	}
	if !hasState {
		bootstrapConfig := raft.Configuration{
			Servers: []raft.Server{{
				Suffrage: raft.Voter,
				ID:       raft.ServerID(id),
				Address:  raft.ServerAddress(addr),
			}},
		}

		err := raft.BootstrapCluster(raftConfig, logCache, store, snapshotStore, transport, bootstrapConfig)
		if err != nil {
			return nil, fmt.Errorf("failed to bootstrap cluster: %w", err)
		}
	}

	rft, err := raft.NewRaft(raftConfig, fsm, logCache, store, snapshotStore, transport)
	if err != nil {
		return nil, fmt.Errorf("failed to create Raft: %w", err)
	}

	return &Raft{
		Raft:      rft,
		transport: transport,
		store:     store,
	}, nil
}

// Shutdown is used to stop the Raft background routines.
// This is not a graceful operation. Provides a future that
// can be used to block until all background routines have exited.
func (r *Raft) Shutdown() raft.Future {
	return &shutdownFuture{
		Future: r.Raft.Shutdown(),
		r:      r,
	}
}

type shutdownFuture struct {
	raft.Future
	r *Raft
}

func (f *shutdownFuture) Error() error {
	err := f.Future.Error()

	err = errors.Join(err, f.r.store.Close())
	err = errors.Join(err, f.r.transport.Close())

	return err
}

func onlyIfError(err error, onErr func() error) error {
	if err != nil {
		return errors.Join(err, onErr())
	}
	return nil
}

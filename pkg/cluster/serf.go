package cluster

import (
	"fmt"
	"log/slog"
	"path/filepath"
	"sync"

	"github.com/hashicorp/memberlist"
	"github.com/hashicorp/serf/serf"
)

type Serf struct {
	*serf.Serf
	EventCh <-chan serf.Event

	shutdownFn func()
}

type SerfTags struct {
	Role     string
	RaftAddr string
}

func CreateSerf(
	id string,
	host string,
	port int,
	dataPath string,
	peers []string,
) (s *Serf, err error) {
	logger := slog.Default()

	serfChan := make(chan serf.Event, 128)

	memberlistConfig := memberlist.DefaultLANConfig()
	memberlistConfig.BindPort = port
	memberlistConfig.Logger = slog.NewLogLogger(logger.With("component", "memberlist").Handler(), slog.LevelInfo)

	serfConfig := serf.DefaultConfig()
	serfConfig.Init()
	serfConfig.NodeName = id
	serfConfig.EventCh = serfChan
	serfConfig.MemberlistConfig = memberlistConfig
	serfConfig.Logger = slog.NewLogLogger(logger.With("component", "serf").Handler(), slog.LevelInfo)
	serfConfig.SnapshotPath = filepath.Join(dataPath, "serf")

	serfConfig.Tags["role"] = "conduit"
	// serfConfig.Tags["raft_addr"] = ???
	// serfConfig.Tags["serf_lan_addr"] = ???
	// serfConfig.Tags["broker_addr"] = ???

	logger.Info("creating serf", "config", serfConfig)
	srf, err := serf.Create(serfConfig)
	if err != nil {
		return nil, fmt.Errorf("failed to create serf: %w", err)
	}
	defer func() { err = onlyIfError(err, srf.Shutdown) }()

	if len(peers) > 0 {
		logger.Info("joining serf", "peers", peers)
		i, err := srf.Join(peers, false)
		if err != nil {
			return nil, fmt.Errorf("failed to join serf: %w", err)
		}
		logger.Info("joined serf", "num", i)
	} else {
		logger.Info("no peers to join")
	}

	var shutdownOnce sync.Once
	return &Serf{
		Serf:       srf,
		EventCh:    serfChan,
		shutdownFn: func() { shutdownOnce.Do(func() { close(serfChan) }) },
	}, nil
}

// Shutdown forcefully shuts down the Serf instance, stopping all network
// activity and background maintenance associated with the instance.
//
// This is not a graceful shutdown, and should be preceded by a call
// to Leave. Otherwise, other nodes in the cluster will detect this node's
// exit as a node failure.
//
// It is safe to call this method multiple times.
func (s *Serf) Shutdown() error {
	defer s.shutdownFn()
	return s.Serf.Shutdown()
}

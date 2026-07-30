package server

import (
	"net/http"

	"github.com/gorilla/mux"

	"github.com/ifantsai/dcnetlab/controller/internal/observer"
)

// TopologyWatcher is the slice of the observer this transport needs:
// the latest snapshot plus a live subscription per lab.
type TopologyWatcher interface {
	Latest(labID string) []observer.Observation
	Subscribe(labID string) (<-chan []observer.Observation, func())
}

// topologyMessage is the frame pushed to topology subscribers.
type topologyMessage struct {
	Type  string                 `json:"type"` // "observations"
	Nodes []observer.Observation `json:"nodes"`
}

// topologyHandler streams observer sweeps of one lab over WebSocket:
// the latest snapshot immediately on connect, then every new sweep.
func topologyHandler(watcher TopologyWatcher) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		labID := mux.Vars(r)["labId"]

		conn, err := terminalUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return // Upgrade already wrote the HTTP error
		}

		defer func() { _ = conn.Close() }()

		sweeps, cancel := watcher.Subscribe(labID)
		defer cancel()

		if latest := watcher.Latest(labID); latest != nil {
			if err := conn.WriteJSON(topologyMessage{Type: "observations", Nodes: latest}); err != nil {
				return
			}
		}

		// Detect client disconnect: reads fail once the peer goes away,
		// which unblocks the write loop below via closed.
		closed := make(chan struct{})
		go func() {
			defer close(closed)

			for {
				if _, _, err := conn.ReadMessage(); err != nil {
					return
				}
			}
		}()

		for {
			select {
			case <-closed:
				return
			case obs := <-sweeps:
				if err := conn.WriteJSON(topologyMessage{Type: "observations", Nodes: obs}); err != nil {
					return
				}
			}
		}
	}
}

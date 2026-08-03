package server

import (
	"encoding/json"
	"net/http"

	kjson "github.com/go-kratos/kratos/v2/encoding/json"
	"github.com/gorilla/mux"

	"github.com/ifantsai/dcnetlab/controller/internal/service"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// TrafficLister is the slice of the traffic usecase this transport
// needs: the current scenarios of a lab.
type TrafficLister interface {
	ListTrafficScenarios(labID string) ([]*model.TrafficScenario, error)
}

// TrafficTicker signals that a collector sweep refreshed a lab's
// scenario states; the traffic collector implements it.
type TrafficTicker interface {
	Subscribe(labID string) (<-chan struct{}, func())
}

// trafficMessage is the frame pushed to traffic subscribers: the full
// scenario list, as protojson payloads byte-compatible with REST.
type trafficMessage struct {
	Type      string            `json:"type"` // "scenarios"
	Scenarios []json.RawMessage `json:"scenarios"`
}

// trafficHandler streams a lab's traffic scenarios over WebSocket:
// the current list on connect, then a fresh list after every
// collector sweep (metrics, assertions and auto-stop all land within
// one sweep interval).
func trafficHandler(lister TrafficLister, ticker TrafficTicker) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		labID := mux.Vars(r)["labId"]

		conn, err := terminalUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return // Upgrade already wrote the HTTP error
		}

		defer func() { _ = conn.Close() }()

		ticks, cancel := ticker.Subscribe(labID)
		defer cancel()

		push := func() error {
			scenarios, err := lister.ListTrafficScenarios(labID)
			if err != nil {
				return err
			}

			msg := trafficMessage{Type: "scenarios", Scenarios: make([]json.RawMessage, 0, len(scenarios))}
			for _, sc := range scenarios {
				raw, err := kjson.MarshalOptions.Marshal(service.TrafficScenarioToPB(sc))
				if err != nil {
					return err
				}

				msg.Scenarios = append(msg.Scenarios, raw)
			}

			return conn.WriteJSON(msg)
		}

		if err := push(); err != nil {
			return
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
			case <-ticks:
				if err := push(); err != nil {
					return
				}
			}
		}
	}
}

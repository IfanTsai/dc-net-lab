package server

import (
	"encoding/json"
	"net/http"

	kjson "github.com/go-kratos/kratos/v2/encoding/json"
	"github.com/gorilla/mux"

	"github.com/ifantsai/dcnetlab/controller/internal/service"
	"github.com/ifantsai/dcnetlab/internal/model"
)

// OperationFeed is the slice of the operation usecase this transport
// needs: the current operations of a lab plus a live subscription.
type OperationFeed interface {
	ListOperations(labID string) ([]*model.Operation, error)
	SubscribeOperations(labID string) (<-chan *model.Operation, func())
}

// operationsMessage is a frame pushed to operation subscribers: the
// full list on connect ("operations"), then one changed operation per
// frame ("operation"). Payloads are protojson, byte-compatible with
// the REST responses.
type operationsMessage struct {
	Type       string            `json:"type"`
	Operations []json.RawMessage `json:"operations,omitempty"`
	Operation  json.RawMessage   `json:"operation,omitempty"`
}

// operationJSON renders one operation exactly like the REST layer
// (same converter, same protojson options).
func operationJSON(op *model.Operation) (json.RawMessage, error) {
	return kjson.MarshalOptions.Marshal(service.OperationToPB(op))
}

// operationsHandler streams a lab's operation state changes over
// WebSocket: a snapshot on connect, then every persisted update. The
// UI follows apply/scale/repair progress on this channel instead of
// polling.
func operationsHandler(feed OperationFeed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		labID := mux.Vars(r)["labId"]

		conn, err := terminalUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return // Upgrade already wrote the HTTP error
		}

		defer func() { _ = conn.Close() }()

		updates, cancel := feed.SubscribeOperations(labID)
		defer cancel()

		ops, err := feed.ListOperations(labID)
		if err != nil {
			return
		}

		snapshot := operationsMessage{Type: "operations", Operations: make([]json.RawMessage, 0, len(ops))}
		for _, op := range ops {
			raw, err := operationJSON(op)
			if err != nil {
				return
			}

			snapshot.Operations = append(snapshot.Operations, raw)
		}

		if err := conn.WriteJSON(snapshot); err != nil {
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
			case op := <-updates:
				raw, err := operationJSON(op)
				if err != nil {
					return
				}

				if err := conn.WriteJSON(operationsMessage{Type: "operation", Operation: raw}); err != nil {
					return
				}
			}
		}
	}
}

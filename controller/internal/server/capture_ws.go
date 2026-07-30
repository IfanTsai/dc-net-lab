package server

import (
	"errors"
	"net/http"

	"github.com/gorilla/mux"

	"github.com/ifantsai/dcnetlab/controller/internal/capture"
)

// CaptureFeed is the slice of the capture usecase this transport
// needs: live packet subscriptions and the recording file for
// download.
type CaptureFeed interface {
	SubscribeCapture(labID, id string) (capture.Event, <-chan capture.Event, func(), error)
	RecordingPath(labID, id string) (path, filename string, err error)
}

// captureHandler streams a capture session's packet rows over
// WebSocket: a snapshot of the current window on connect, then
// batched events until the session ends. Events carry the capture
// indices, so a client that misses a batch (slow consumer) sees the
// gap and can point at the pcap download.
func captureHandler(feed CaptureFeed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		snapshot, events, cancel, err := feed.SubscribeCapture(vars["labId"], vars["id"])
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, capture.ErrRecordingGone) {
				status = http.StatusGone
			}

			http.Error(w, err.Error(), status)

			return
		}

		defer cancel()

		conn, err := terminalUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return // Upgrade already wrote the HTTP error
		}

		defer func() { _ = conn.Close() }()

		if err := conn.WriteJSON(snapshot); err != nil {
			return
		}

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
			case ev, ok := <-events:
				if !ok {
					return
				}

				if err := conn.WriteJSON(ev); err != nil {
					return
				}
			}
		}
	}
}

// capturePcapHandler serves a session's pcapng recording for download
// (Wireshark-ready).
func capturePcapHandler(feed CaptureFeed) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		path, filename, err := feed.RecordingPath(vars["labId"], vars["id"])
		if err != nil {
			status := http.StatusBadRequest
			if errors.Is(err, capture.ErrRecordingGone) {
				status = http.StatusGone
			}

			http.Error(w, err.Error(), status)

			return
		}

		w.Header().Set("Content-Type", "application/x-pcapng")
		w.Header().Set("Content-Disposition", `attachment; filename="`+filename+`"`)
		http.ServeFile(w, r, path)
	}
}

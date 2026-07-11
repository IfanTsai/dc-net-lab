package server

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"

	"github.com/go-kratos/kratos/v2/log"
	"github.com/gorilla/mux"
	"github.com/gorilla/websocket"

	"github.com/ifantsai/dcnetlab/internal/runtime"
)

// TerminalOpener is the slice of the terminal usecase this transport
// needs: turn a lab/node pair into a live shell session.
type TerminalOpener interface {
	OpenNodeTerminal(ctx context.Context, labID, nodeID string) (runtime.TerminalSession, error)
}

// terminalMessage is the JSON control frame of the terminal protocol.
// Terminal data itself travels as binary frames in both directions.
type terminalMessage struct {
	Type string `json:"type"` // "resize" (client), "error" (server)
	Cols uint16 `json:"cols,omitempty"`
	Rows uint16 `json:"rows,omitempty"`

	Message string `json:"message,omitempty"`
}

// terminalUpgrader accepts any origin: the controller binds to
// localhost and serves the UI itself, so CSRF via origin does not add
// protection here.
var terminalUpgrader = websocket.Upgrader{
	CheckOrigin: func(*http.Request) bool { return true },
}

// terminalHandler bridges a WebSocket to an interactive node shell.
// Errors are reported in-band as an "error" control frame so the
// browser can show them, then the socket is closed.
func terminalHandler(opener TerminalOpener, logger log.Logger) http.HandlerFunc {
	helper := log.NewHelper(logger)

	return func(w http.ResponseWriter, r *http.Request) {
		vars := mux.Vars(r)

		conn, err := terminalUpgrader.Upgrade(w, r, nil)
		if err != nil {
			return // Upgrade already wrote the HTTP error
		}

		defer func() { _ = conn.Close() }()

		// The session must outlive the request context: Kratos caps it
		// with its server timeout, which would kill the shell after a
		// second. Lifetime is owned by the Close calls below instead.
		session, err := opener.OpenNodeTerminal(context.WithoutCancel(r.Context()), vars["labId"], vars["nodeId"])
		if err != nil {
			msg := err.Error()
			if errors.Is(err, runtime.ErrNotSupported) {
				msg = "terminal requires the containerlab runtime (current runtime does not support it)"
			}

			_ = conn.WriteJSON(terminalMessage{Type: "error", Message: msg})

			return
		}

		defer func() { _ = session.Close() }()

		// Shell output → WebSocket. Closing the session (below) makes
		// the pending Read fail, so this goroutine always exits.
		done := make(chan struct{})
		go func() {
			defer close(done)

			buf := make([]byte, 4096)
			for {
				n, err := session.Read(buf)
				if n > 0 {
					if err := conn.WriteMessage(websocket.BinaryMessage, buf[:n]); err != nil {
						return
					}
				}

				if err != nil {
					_ = conn.WriteMessage(websocket.CloseMessage,
						websocket.FormatCloseMessage(websocket.CloseNormalClosure, "session ended"))

					return
				}
			}
		}()

		// WebSocket → shell: binary frames are keystrokes, text frames
		// are JSON control messages.
		for {
			kind, data, err := conn.ReadMessage()
			if err != nil {
				break
			}

			switch kind {
			case websocket.BinaryMessage:
				if _, err := session.Write(data); err != nil {
					helper.Warnf("terminal write: %v", err)
				}

			case websocket.TextMessage:
				var msg terminalMessage
				if json.Unmarshal(data, &msg) == nil && msg.Type == "resize" {
					_ = session.Resize(msg.Cols, msg.Rows)
				}
			}
		}

		_ = session.Close()
		<-done
	}
}

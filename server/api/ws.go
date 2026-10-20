package api

import (
	"encoding/json"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/gorilla/websocket"
)

// The app keeps one WebSocket to /v1/ws for live status. Browsers can't set
// headers on a WebSocket, so the first message authenticates:
//
//	→ {"type":"auth","token":"sms_…"}
//	← {"type":"status","relays":{…}}   now, and whenever a relay changes
//	← {"type":"changed"}               relays/deployments changed: refetch

type watchers struct {
	mu    sync.Mutex
	conns map[*watcher]struct{}
}

type watcher struct {
	ws *websocket.Conn
	mu sync.Mutex
}

func newWatchers() *watchers { return &watchers{conns: map[*watcher]struct{}{}} }

func (w *watcher) write(msg []byte) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	_ = w.ws.SetWriteDeadline(time.Now().Add(10 * time.Second))
	return w.ws.WriteMessage(websocket.TextMessage, msg)
}

func (ws *watchers) send(v any) {
	msg, err := json.Marshal(v)
	if err != nil {
		return
	}
	ws.mu.Lock()
	list := make([]*watcher, 0, len(ws.conns))
	for w := range ws.conns {
		list = append(list, w)
	}
	ws.mu.Unlock()
	for _, w := range list {
		if w.write(msg) != nil {
			w.ws.Close()
		}
	}
}

func (a *API) statusMsg() gin.H {
	return gin.H{"type": "status", "relays": a.hub.State()}
}

func (a *API) broadcastStatus() { a.watchers.send(a.statusMsg()) }

func (a *API) status(c *gin.Context) { c.JSON(http.StatusOK, a.statusMsg()) }

func (a *API) ws(c *gin.Context) {
	conn, err := upgrader.Upgrade(c.Writer, c.Request, nil)
	if err != nil {
		return
	}
	defer conn.Close()
	conn.SetReadLimit(4096)
	_ = conn.SetReadDeadline(time.Now().Add(10 * time.Second))
	var auth struct {
		Type  string `json:"type"`
		Token string `json:"token"`
	}
	if err := conn.ReadJSON(&auth); err != nil || auth.Type != "auth" || !a.validSession(auth.Token) {
		_ = conn.WriteMessage(websocket.CloseMessage, websocket.FormatCloseMessage(4001, "unauthorized"))
		return
	}

	w := &watcher{ws: conn}
	msg, _ := json.Marshal(a.statusMsg())
	if w.write(msg) != nil {
		return
	}
	a.watchers.mu.Lock()
	a.watchers.conns[w] = struct{}{}
	a.watchers.mu.Unlock()
	defer func() {
		a.watchers.mu.Lock()
		delete(a.watchers.conns, w)
		a.watchers.mu.Unlock()
	}()

	// Keepalive: ping every 30s; the client must answer within 75s.
	_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))
	conn.SetPongHandler(func(string) error { return conn.SetReadDeadline(time.Now().Add(75 * time.Second)) })
	done := make(chan struct{})
	defer close(done)
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for {
			select {
			case <-done:
				return
			case <-t.C:
				w.mu.Lock()
				err := conn.WriteControl(websocket.PingMessage, nil, time.Now().Add(10*time.Second))
				w.mu.Unlock()
				if err != nil {
					conn.Close()
					return
				}
			}
		}
	}()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
		_ = conn.SetReadDeadline(time.Now().Add(75 * time.Second))
	}
}

func (ws *watchers) any() bool {
	ws.mu.Lock()
	defer ws.mu.Unlock()
	return len(ws.conns) > 0
}

// statusTicker re-sends status every 10 s while anyone's watching and a
// relay is online, so round-trip times and byte counts stay live.
func (a *API) statusTicker() {
	for range time.Tick(10 * time.Second) {
		if a.watchers.any() && len(a.hub.Online()) > 0 {
			a.broadcastStatus()
		}
	}
}

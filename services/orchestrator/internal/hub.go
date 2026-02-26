package internal

import (
	"context"
	"encoding/json"
	"sync"
	"time"

	"github.com/forge-ai/forge/shared/events"
	"github.com/gorilla/websocket"
	"github.com/rs/zerolog/log"
	"net/http"
)

type Hub struct {
	mu      sync.RWMutex
	clients map[*wsConn]struct{}
	bc      chan []byte
}

type wsConn struct {
	conn *websocket.Conn
	send chan []byte
}

func NewHub() *Hub {
	return &Hub{
		clients: make(map[*wsConn]struct{}),
		bc:      make(chan []byte, 512),
	}
}

func (h *Hub) Run(ctx context.Context) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case msg := <-h.bc:
			h.mu.RLock()
			for c := range h.clients {
				select {
				case c.send <- msg:
				default:
				}
			}
			h.mu.RUnlock()
		}
	}
}

func (h *Hub) Broadcast(env *events.Envelope) {
	b, _ := json.Marshal(env)
	h.BroadcastRaw(b)
}

func (h *Hub) BroadcastRaw(b []byte) {
	select {
	case h.bc <- b:
	default:
	}
}

var upgrader = websocket.Upgrader{
	CheckOrigin:    func(r *http.Request) bool { return true },
	ReadBufferSize: 1024, WriteBufferSize: 4096,
}

func (h *Hub) ServeWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		log.Error().Err(err).Msg("WS upgrade failed")
		return
	}
	c := &wsConn{conn: conn, send: make(chan []byte, 64)}
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()

	go func() {
		defer func() {
			conn.Close()
			h.mu.Lock()
			delete(h.clients, c)
			h.mu.Unlock()
		}()
		for msg := range c.send {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if conn.WriteMessage(websocket.TextMessage, msg) != nil {
				return
			}
		}
	}()

	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

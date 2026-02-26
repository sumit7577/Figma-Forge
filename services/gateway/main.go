// gateway is the public-facing HTTP service.
// It accepts job submissions from the React frontend,
// publishes them to RabbitMQ (job.submitted),
// and relays all log.event / screen.done / job.done messages
// to connected browsers over WebSocket.
package main

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"os"
	"os/signal"
	"sync"
	"syscall"
	"time"

	"github.com/forge-ai/forge/shared/events"
	"github.com/forge-ai/forge/shared/mq"
	"github.com/google/uuid"
	"github.com/gorilla/websocket"
	"github.com/joho/godotenv"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	_ = godotenv.Load()

	amqpURL     := envOr("AMQP_URL", "amqp://forge:forge@rabbitmq:5672/")
	port        := envOr("PORT", "8080")
	supabaseURL := envOr("SUPABASE_URL", "")
	supabaseKey := envOr("SUPABASE_SERVICE_KEY", "")

	broker, err := mq.New(amqpURL)
	if err != nil {
		log.Fatal().Err(err).Msg("mq connect")
	}
	defer broker.Close()

	gw := &gateway{
		broker:      broker,
		hub:         newHub(),
		supabaseURL: supabaseURL,
		supabaseKey: supabaseKey,
		httpClient:  &http.Client{Timeout: 10 * time.Second},
	}

	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigs; cancel() }()

	go gw.hub.run(ctx)
	go gw.subscribeEvents(ctx)

	mux := http.NewServeMux()

	// REST
	mux.HandleFunc("POST /api/jobs",              gw.createJob)
	mux.HandleFunc("GET /api/jobs",               gw.listJobs)
	mux.HandleFunc("GET /api/jobs/{id}",          gw.getJob)
	mux.HandleFunc("GET /api/jobs/{id}/screens",  gw.getScreens)
	mux.HandleFunc("GET /api/status",             gw.status)

	// WebSocket
	mux.HandleFunc("/ws", gw.serveWS)

	// Serve React build
	mux.Handle("/", http.FileServer(http.Dir("/app/web/dist")))

	srv := &http.Server{
		Addr:    ":" + port,
		Handler: cors(mux),
	}

	log.Info().Str("port", port).Msg("gateway online")

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		log.Fatal().Err(err).Msg("server error")
	}
}

// ── Gateway ───────────────────────────────────────────────────────────────────

type gateway struct {
	broker      *mq.Broker
	hub         *hub
	supabaseURL string
	supabaseKey string
	httpClient  *http.Client
}

func (gw *gateway) createJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FigmaURL  string   `json:"figma_url"`
		RepoURL   string   `json:"repo_url"`
		Platforms []string `json:"platforms"`
		Styling   string   `json:"styling"`
		Threshold int      `json:"threshold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, "invalid body", 400)
		return
	}
	if req.FigmaURL == "" {
		jsonErr(w, "figma_url required", 400)
		return
	}
	if len(req.Platforms) == 0 {
		req.Platforms = []string{events.PlatformReact, events.PlatformKMP}
	}
	if req.Styling == "" {
		req.Styling = "tailwind"
	}
	if req.Threshold == 0 {
		req.Threshold = 95
	}

	jobID := uuid.New().String()
	payload := events.JobSubmittedPayload{
		JobID:     jobID,
		FigmaURL:  req.FigmaURL,
		RepoURL:   req.RepoURL,
		Platforms: req.Platforms,
		Styling:   req.Styling,
		Threshold: req.Threshold,
	}

	b, _ := events.Wrap(events.JobSubmitted, payload)
	if err := gw.broker.Publish(r.Context(), events.JobSubmitted, b); err != nil {
		jsonErr(w, "queue publish failed", 500)
		return
	}

	jsonOK(w, map[string]any{
		"job_id":    jobID,
		"platforms": req.Platforms,
		"status":    "queued",
	}, 201)
}

func (gw *gateway) listJobs(w http.ResponseWriter, r *http.Request) {
	jobs := gw.supabaseQuery(r.Context(), "jobs?order=created_at.desc&limit=50")
	jsonOK(w, jobs, 200)
}

func (gw *gateway) getJob(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	jobs := gw.supabaseQuery(r.Context(), "jobs?id=eq."+id)
	if len(jobs) == 0 {
		jsonErr(w, "not found", 404)
		return
	}
	jsonOK(w, jobs[0], 200)
}

func (gw *gateway) getScreens(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	screens := gw.supabaseQuery(r.Context(), "iterations?job_id=eq."+id+"&order=created_at.asc")
	jsonOK(w, screens, 200)
}

func (gw *gateway) status(w http.ResponseWriter, r *http.Request) {
	jsonOK(w, map[string]any{
		"status":   "online",
		"clients":  gw.hub.clientCount(),
		"version":  "0.2.0",
	}, 200)
}

// supabaseQuery is a simple REST GET wrapper
func (gw *gateway) supabaseQuery(ctx context.Context, path string) []map[string]any {
	if gw.supabaseURL == "" {
		return nil
	}
	req, _ := http.NewRequestWithContext(ctx, "GET", gw.supabaseURL+"/rest/v1/"+path, nil)
	req.Header.Set("apikey", gw.supabaseKey)
	req.Header.Set("Authorization", "Bearer "+gw.supabaseKey)
	resp, err := gw.httpClient.Do(req)
	if err != nil {
		return nil
	}
	defer resp.Body.Close()
	var result []map[string]any
	json.NewDecoder(resp.Body).Decode(&result)
	return result
}

// subscribeEvents relays all forge events to WebSocket clients.
func (gw *gateway) subscribeEvents(ctx context.Context) {
	patterns := []struct{ q, p string }{
		{"gw.log.relay",   "log.#"},
		{"gw.screen.done", events.ScreenDone},
		{"gw.job.done",    events.JobDone},
		{"gw.job.failed",  events.JobFailed},
	}
	for _, sub := range patterns {
		sub := sub
		deliveries, err := gw.broker.Subscribe(sub.q, sub.p)
		if err != nil {
			log.Error().Err(err).Str("queue", sub.q).Msg("subscribe failed")
			continue
		}
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				case d, ok := <-deliveries:
					if !ok {
						return
					}
					gw.hub.broadcast(d.Body)
					d.Ack(false)
				}
			}
		}()
	}
}

// ── WebSocket hub ─────────────────────────────────────────────────────────────

var upgrader = websocket.Upgrader{
	CheckOrigin:     func(r *http.Request) bool { return true },
	ReadBufferSize:  1024,
	WriteBufferSize: 4096,
}

type wsClient struct {
	conn *websocket.Conn
	send chan []byte
}

type hub struct {
	mu      sync.RWMutex
	clients map[*wsClient]struct{}
	bc      chan []byte
}

func newHub() *hub {
	return &hub{
		clients: make(map[*wsClient]struct{}),
		bc:      make(chan []byte, 512),
	}
}

func (h *hub) run(ctx context.Context) {
	for {
		select {
		case <-ctx.Done():
			return
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

func (h *hub) broadcast(msg []byte) {
	select {
	case h.bc <- msg:
	default:
	}
}

func (h *hub) clientCount() int {
	h.mu.RLock()
	defer h.mu.RUnlock()
	return len(h.clients)
}

func (gw *gateway) serveWS(w http.ResponseWriter, r *http.Request) {
	conn, err := upgrader.Upgrade(w, r, nil)
	if err != nil {
		return
	}
	c := &wsClient{conn: conn, send: make(chan []byte, 64)}
	gw.hub.mu.Lock()
	gw.hub.clients[c] = struct{}{}
	gw.hub.mu.Unlock()

	log.Debug().Str("remote", r.RemoteAddr).Msg("WS connected")

	// Write pump
	go func() {
		defer func() {
			conn.Close()
			gw.hub.mu.Lock()
			delete(gw.hub.clients, c)
			gw.hub.mu.Unlock()
		}()
		for msg := range c.send {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if err := conn.WriteMessage(websocket.TextMessage, msg); err != nil {
				return
			}
		}
	}()

	// Ping/pong keepalive
	conn.SetReadDeadline(time.Now().Add(60 * time.Second))
	conn.SetPongHandler(func(string) error {
		conn.SetReadDeadline(time.Now().Add(60 * time.Second))
		return nil
	})
	go func() {
		t := time.NewTicker(30 * time.Second)
		defer t.Stop()
		for range t.C {
			conn.SetWriteDeadline(time.Now().Add(10 * time.Second))
			if conn.WriteMessage(websocket.PingMessage, nil) != nil {
				return
			}
		}
	}()
	for {
		if _, _, err := conn.ReadMessage(); err != nil {
			return
		}
	}
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func jsonOK(w http.ResponseWriter, v any, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(v)
}

func jsonErr(w http.ResponseWriter, msg string, code int) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

func cors(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,PUT,DELETE,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type,Authorization")
		if r.Method == "OPTIONS" {
			w.WriteHeader(204)
			return
		}
		next.ServeHTTP(w, r)
	})
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

// suppress unused import
var _ = io.ReadAll

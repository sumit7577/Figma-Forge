package internal

import (
	"context"
	"encoding/json"
	"net/http"
	"time"

	"github.com/forge-ai/forge/shared/events"
	"github.com/google/uuid"
)

func (o *Orchestrator) serveAPI(ctx context.Context) error {
	mux := http.NewServeMux()

	mux.HandleFunc("POST /api/jobs", o.handleCreateJob)
	mux.HandleFunc("GET /api/status", o.handleStatus)
	mux.HandleFunc("/ws", o.hub.ServeWS)

	srv := &http.Server{
		Addr:         ":" + o.cfg.APIPort,
		Handler:      cors(mux),
		ReadTimeout:  15 * time.Second,
		WriteTimeout: 15 * time.Second,
	}

	go func() {
		<-ctx.Done()
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		srv.Shutdown(shutCtx)
	}()

	if err := srv.ListenAndServe(); err != http.ErrServerClosed {
		return err
	}
	return nil
}

func (o *Orchestrator) handleCreateJob(w http.ResponseWriter, r *http.Request) {
	var req struct {
		FigmaURL  string   `json:"figma_url"`
		RepoURL   string   `json:"repo_url"`
		Platforms []string `json:"platforms"`
		Styling   string   `json:"styling"`
		Threshold int      `json:"threshold"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		jsonErr(w, "invalid body", 400); return
	}
	if req.FigmaURL == "" { jsonErr(w, "figma_url required", 400); return }
	if len(req.Platforms) == 0 { req.Platforms = []string{events.PlatformReact, events.PlatformKMP} }
	if req.Styling   == "" { req.Styling = "tailwind" }
	if req.Threshold == 0  { req.Threshold = o.cfg.DefaultThreshold }

	p := events.JobSubmittedPayload{
		JobID: uuid.New().String(), FigmaURL: req.FigmaURL,
		RepoURL: req.RepoURL, Platforms: req.Platforms,
		Styling: req.Styling, Threshold: req.Threshold,
	}
	b, _ := events.Wrap(events.JobSubmitted, p)
	if err := o.broker.Publish(r.Context(), events.JobSubmitted, b); err != nil {
		jsonErr(w, "queue error", 500); return
	}
	jsonOK(w, map[string]any{"job_id": p.JobID, "status": "queued"}, 201)
}

func (o *Orchestrator) handleStatus(w http.ResponseWriter, r *http.Request) {
	o.mu.RLock()
	active := len(o.jobs)
	o.mu.RUnlock()
	jsonOK(w, map[string]any{"status": "online", "active_jobs": active}, 200)
}

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
		w.Header().Set("Access-Control-Allow-Methods", "GET,POST,OPTIONS")
		w.Header().Set("Access-Control-Allow-Headers", "Content-Type")
		if r.Method == "OPTIONS" { w.WriteHeader(204); return }
		next.ServeHTTP(w, r)
	})
}

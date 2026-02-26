package internal

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"

	"github.com/forge-ai/forge/shared/events"
	"github.com/forge-ai/forge/shared/mq"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog/log"
	"golang.org/x/sync/errgroup"
)

// screenKey identifies a unique screen×platform work unit.
type screenKey struct {
	JobID       string
	ScreenIndex int
	Platform    string
}

// screenState tracks iteration progress per screen×platform.
type screenState struct {
	mu         sync.Mutex
	Iteration  int
	BestScore  float64
	BestCode   string
	Done       bool
}

// jobState tracks overall job progress.
type jobState struct {
	mu            sync.Mutex
	Platforms     []string
	Screens       []events.FigmaScreen
	ScreenStates  map[screenKey]*screenState
	TotalWork     int // screens × platforms
	Completed     int
	TotalScore    float64
	TotalIter     int
	RepoContext   string
	Threshold     int
}

// Orchestrator subscribes to the topic exchange and drives the full pipeline.
type Orchestrator struct {
	cfg    Config
	broker *mq.Broker
	hub    *Hub   // WebSocket broadcast to frontend
	store  *Store // Supabase

	mu   sync.RWMutex
	jobs map[string]*jobState
}

func NewOrchestrator(cfg Config) (*Orchestrator, error) {
	broker, err := mq.New(cfg.AMQPURL)
	if err != nil {
		return nil, fmt.Errorf("mq connect: %w", err)
	}

	store := NewStore(cfg.SupabaseURL, cfg.SupabaseKey)
	hub := NewHub()

	return &Orchestrator{
		cfg:    cfg,
		broker: broker,
		hub:    hub,
		store:  store,
		jobs:   make(map[string]*jobState),
	}, nil
}

func (o *Orchestrator) Close() {
	o.broker.Close()
}

// Run starts all consumers and the API server.
func (o *Orchestrator) Run(ctx context.Context) error {
	g, ctx := errgroup.WithContext(ctx)

	// WebSocket hub
	g.Go(func() error { return o.hub.Run(ctx) })

	// API server (REST + WS)
	g.Go(func() error { return o.serveAPI(ctx) })

	// Subscribe to every event the orchestrator cares about
	subs := []struct {
		queue   string
		pattern string
		handler func(context.Context, amqp.Delivery) error
	}{
		{"orch.job.submitted",    events.JobSubmitted,    o.onJobSubmitted},
		{"orch.figma.parsed",     events.FigmaParsed,     o.onFigmaParsed},
		{"orch.figma.failed",     events.FigmaFailed,     o.onFigmaFailed},
		{"orch.codegen.complete", events.CodegenComplete,  o.onCodegenComplete},
		{"orch.codegen.failed",   events.CodegenFailed,    o.onCodegenFailed},
		{"orch.sandbox.ready",    events.SandboxReady,    o.onSandboxReady},
		{"orch.sandbox.failed",   events.SandboxFailed,   o.onSandboxFailed},
		{"orch.diff.complete",    events.DiffComplete,    o.onDiffComplete},
		{"orch.diff.failed",      events.DiffFailed,      o.onDiffFailed},
		// Forward all log events to WS hub
		{"orch.log.relay",        "log.#",                o.onLogRelay},
	}

	for _, sub := range subs {
		sub := sub
		deliveries, err := o.broker.Subscribe(sub.queue, sub.pattern)
		if err != nil {
			return fmt.Errorf("subscribe %s: %w", sub.queue, err)
		}
		g.Go(func() error {
			return o.consume(ctx, deliveries, sub.handler)
		})
	}

	return g.Wait()
}

// consume is the generic delivery loop for all subscriptions.
func (o *Orchestrator) consume(
	ctx context.Context,
	deliveries <-chan amqp.Delivery,
	handler func(context.Context, amqp.Delivery) error,
) error {
	for {
		select {
		case <-ctx.Done():
			return nil
		case d, ok := <-deliveries:
			if !ok {
				return fmt.Errorf("delivery channel closed")
			}
			if err := handler(ctx, d); err != nil {
				log.Error().Err(err).Str("key", d.RoutingKey).Msg("handler error")
				d.Nack(false, true) // requeue
			} else {
				d.Ack(false)
			}
		}
	}
}

// ── Event Handlers ────────────────────────────────────────────────────────────

func (o *Orchestrator) onJobSubmitted(ctx context.Context, d amqp.Delivery) error {
	p, err := events.Unwrap[events.JobSubmittedPayload](d.Body)
	if err != nil {
		return err
	}

	o.emitLog(ctx, p.JobID, "info", "job_submitted",
		fmt.Sprintf("Job received — platforms: %v", p.Platforms), nil)

	// Create job state
	js := &jobState{
		Platforms:    p.Platforms,
		ScreenStates: make(map[screenKey]*screenState),
		Threshold:    p.Threshold,
	}
	o.mu.Lock()
	o.jobs[p.JobID] = js
	o.mu.Unlock()

	// Persist to Supabase
	_ = o.store.CreateJob(ctx, p)

	// Request Figma parse
	return o.publish(ctx, events.ParseFigmaRequested,
		events.ParseFigmaRequestedPayload{
			JobID:    p.JobID,
			FigmaURL: p.FigmaURL,
		})
}

func (o *Orchestrator) onFigmaParsed(ctx context.Context, d amqp.Delivery) error {
	p, err := events.Unwrap[events.FigmaParsedPayload](d.Body)
	if err != nil {
		return err
	}

	o.mu.Lock()
	js, ok := o.jobs[p.JobID]
	if !ok {
		o.mu.Unlock()
		return fmt.Errorf("job %s not found in state", p.JobID)
	}
	js.Screens = p.Screens
	js.TotalWork = len(p.Screens) * len(js.Platforms)
	// Initialise screen states
	for i := range p.Screens {
		for _, platform := range js.Platforms {
			js.ScreenStates[screenKey{p.JobID, i, platform}] = &screenState{}
		}
	}
	o.mu.Unlock()

	o.emitLog(ctx, p.JobID, "success", "figma_parsed",
		fmt.Sprintf("✓ %d screens detected: %s", p.ScreenCount, p.FileName), map[string]any{
			"screens":   p.ScreenCount,
			"platforms": js.Platforms,
		})

	_ = o.store.UpdateJobScreenCount(ctx, p.JobID, p.ScreenCount)

	// Fan out: request codegen for screen[0] × all platforms
	// (screens are processed sequentially per platform, in parallel across platforms)
	if len(p.Screens) == 0 {
		return o.completeJob(ctx, p.JobID)
	}

	for _, platform := range js.Platforms {
		if err := o.requestCodegen(ctx, p.JobID, 0, platform, p.Screens[0], nil, 1); err != nil {
			return err
		}
	}
	return nil
}

func (o *Orchestrator) onFigmaFailed(ctx context.Context, d amqp.Delivery) error {
	p, err := events.Unwrap[events.FigmaFailedPayload](d.Body)
	if err != nil {
		return err
	}
	o.emitLog(ctx, p.JobID, "error", "figma_failed", "Figma parse failed: "+p.Error, nil)
	_ = o.store.MarkJobFailed(ctx, p.JobID, p.Error)
	return o.publish(ctx, events.JobFailed, events.JobFailedPayload{
		JobID: p.JobID,
		Error: p.Error,
		Step:  "figma_parse",
	})
}

func (o *Orchestrator) onCodegenComplete(ctx context.Context, d amqp.Delivery) error {
	p, err := events.Unwrap[events.CodegenCompletePayload](d.Body)
	if err != nil {
		return err
	}

	o.emitLog(ctx, p.JobID, "info", "codegen_complete",
		fmt.Sprintf("[%s] iter %d — code generated (%d bytes)", p.Platform, p.Iteration, len(p.Code)), nil)

	// Forward to sandbox
	return o.publish(ctx, events.SandboxBuildRequested,
		events.SandboxBuildRequestedPayload{
			JobID:       p.JobID,
			ScreenIndex: p.ScreenIndex,
			Platform:    p.Platform,
			Iteration:   p.Iteration,
			Code:        p.Code,
			Filename:    p.Filename,
			Threshold:   p.Threshold,
			Screen:      p.Screen,
		})
}

func (o *Orchestrator) onCodegenFailed(ctx context.Context, d amqp.Delivery) error {
	p, err := events.Unwrap[events.CodegenFailedPayload](d.Body)
	if err != nil {
		return err
	}
	o.emitLog(ctx, p.JobID, "error", "codegen_failed",
		fmt.Sprintf("[%s] codegen error: %s", p.Platform, p.Error), nil)
	// Don't fail the whole job — skip this screen×platform
	return o.advanceOrComplete(ctx, p.JobID, p.ScreenIndex, p.Platform, 0, 0, "")
}

func (o *Orchestrator) onSandboxReady(ctx context.Context, d amqp.Delivery) error {
	p, err := events.Unwrap[events.SandboxReadyPayload](d.Body)
	if err != nil {
		return err
	}

	o.emitLog(ctx, p.JobID, "info", "sandbox_ready",
		fmt.Sprintf("[%s] sandbox running on port %d", p.Platform, p.Port), nil)

	return o.publish(ctx, events.DiffRequested,
		events.DiffRequestedPayload{
			JobID:          p.JobID,
			ScreenIndex:    p.ScreenIndex,
			Platform:       p.Platform,
			Iteration:      p.Iteration,
			SandboxURL:     p.URL,
			ContainerID:    p.ContainerID,
			FigmaExportURL: p.Screen.ExportURL,
			Screen:         p.Screen,
			Threshold:      p.Threshold,
		})
}

func (o *Orchestrator) onSandboxFailed(ctx context.Context, d amqp.Delivery) error {
	p, err := events.Unwrap[events.SandboxFailedPayload](d.Body)
	if err != nil {
		return err
	}
	o.emitLog(ctx, p.JobID, "warn", "sandbox_failed",
		fmt.Sprintf("[%s] build failed — skipping: %s", p.Platform, p.Error), nil)
	return o.advanceOrComplete(ctx, p.JobID, p.ScreenIndex, p.Platform, 0, 0, "")
}

func (o *Orchestrator) onDiffComplete(ctx context.Context, d amqp.Delivery) error {
	p, err := events.Unwrap[events.DiffCompletePayload](d.Body)
	if err != nil {
		return err
	}

	o.emitLog(ctx, p.JobID, func() string {
		if p.Diff.Score >= float64(p.Threshold) {
			return "success"
		}
		return "warn"
	}(), "diff_result",
		fmt.Sprintf("[%s] iter %d — score: %.1f%% (layout:%.0f%% typo:%.0f%% spacing:%.0f%% color:%.0f%%)",
			p.Platform, p.Iteration, p.Diff.Score,
			p.Diff.Layout, p.Diff.Typography, p.Diff.Spacing, p.Diff.Color),
		map[string]any{"score": p.Diff.Score, "passed": p.Passed})

	// Update best score
	o.mu.Lock()
	js := o.jobs[p.JobID]
	o.mu.Unlock()

	if js == nil {
		return fmt.Errorf("job state not found: %s", p.JobID)
	}

	key := screenKey{p.JobID, p.ScreenIndex, p.Platform}
	js.mu.Lock()
	ss := js.ScreenStates[key]
	js.mu.Unlock()

	if ss == nil {
		return fmt.Errorf("screen state not found")
	}

	ss.mu.Lock()
	ss.Iteration = p.Iteration
	if p.Diff.Score > ss.BestScore {
		ss.BestScore = p.Diff.Score
	}
	ss.mu.Unlock()

	// Kill sandbox regardless
	_ = o.killSandbox(ctx, p.ContainerID)

	// Save iteration to Supabase
	_ = o.store.SaveIteration(ctx, *p)

	if p.Passed {
		// ✅ Screen passed
		o.emitLog(ctx, p.JobID, "success", "screen_passed",
			fmt.Sprintf("✅ [%s] %s — %.1f%% in %d iterations",
				p.Platform, p.Screen.Name, p.Diff.Score, p.Iteration), nil)

		_ = o.publish(ctx, events.NotifyRequested, events.NotifyRequestedPayload{
			JobID:        p.JobID,
			ScreenName:   p.Screen.Name,
			Platform:     p.Platform,
			Score:        p.Diff.Score,
			Iterations:   p.Iteration,
			DiffImageURL: p.Diff.DiffImageURL,
		})

		return o.advanceOrComplete(ctx, p.JobID, p.ScreenIndex, p.Platform, p.Diff.Score, p.Iteration, "")
	}

	// Not passed — check max iterations
	maxIter := o.cfg.MaxIter
	if p.Iteration >= maxIter {
		o.emitLog(ctx, p.JobID, "warn", "max_iter",
			fmt.Sprintf("⚠ [%s] max iterations reached (best: %.1f%%) — moving on", p.Platform, p.Diff.Score), nil)
		return o.advanceOrComplete(ctx, p.JobID, p.ScreenIndex, p.Platform, p.Diff.Score, p.Iteration, "")
	}

	// Refine — show diff regions
	for _, r := range p.Diff.Regions {
		o.emitLog(ctx, p.JobID, "info", "diff_region",
			fmt.Sprintf("  ↳ %s: found %q, expected %q", r.Property, r.Actual, r.Expected), nil)
	}

	o.emitLog(ctx, p.JobID, "info", "refining",
		fmt.Sprintf("[%s] %.1f%% < %d%% — refining (iter %d → %d)…",
			p.Platform, p.Diff.Score, p.Threshold, p.Iteration, p.Iteration+1), nil)

	// Feed diff back to codegen for next iteration
	return o.requestCodegen(ctx, p.JobID, p.ScreenIndex, p.Platform, p.Screen, &p.Diff, p.Iteration+1)
}

func (o *Orchestrator) onDiffFailed(ctx context.Context, d amqp.Delivery) error {
	p, err := events.Unwrap[events.DiffFailedPayload](d.Body)
	if err != nil {
		return err
	}
	o.emitLog(ctx, p.JobID, "error", "diff_failed",
		fmt.Sprintf("[%s] diff error: %s", p.Platform, p.Error), nil)
	return o.advanceOrComplete(ctx, p.JobID, p.ScreenIndex, p.Platform, 0, 0, "")
}

func (o *Orchestrator) onLogRelay(ctx context.Context, d amqp.Delivery) error {
	// Forward raw event to WebSocket hub for frontend
	env, err := events.UnwrapEnvelope(d.Body)
	if err != nil {
		return nil // non-fatal
	}
	o.hub.Broadcast(env)
	return nil
}

// ── Helpers ───────────────────────────────────────────────────────────────────

func (o *Orchestrator) requestCodegen(
	ctx context.Context,
	jobID string, screenIdx int, platform string,
	screen events.FigmaScreen, prevDiff *events.DiffResult, iteration int,
) error {
	o.mu.RLock()
	js := o.jobs[jobID]
	o.mu.RUnlock()

	threshold := o.cfg.DefaultThreshold
	repoCtx := ""
	if js != nil {
		threshold = js.Threshold
		repoCtx = js.RepoContext
	}

	o.emitLog(ctx, jobID, "info", "codegen_start",
		fmt.Sprintf("[%s] iter %d — generating %s…", platform, iteration, screen.Name), nil)

	return o.publish(ctx, events.CodegenRequested, events.CodegenRequestedPayload{
		JobID:       jobID,
		ScreenIndex: screenIdx,
		Screen:      screen,
		Platform:    platform,
		Styling:     "tailwind",
		RepoContext: repoCtx,
		PrevDiff:    prevDiff,
		Iteration:   iteration,
		Threshold:   threshold,
	})
}

// advanceOrComplete marks a screen×platform done, then either starts
// the next screen×platform or completes the whole job.
func (o *Orchestrator) advanceOrComplete(
	ctx context.Context,
	jobID string, screenIdx int, platform string,
	score float64, iterations int, code string,
) error {
	o.mu.Lock()
	js, ok := o.jobs[jobID]
	if !ok {
		o.mu.Unlock()
		return nil
	}

	key := screenKey{jobID, screenIdx, platform}
	ss := js.ScreenStates[key]
	if ss != nil {
		ss.mu.Lock()
		ss.Done = true
		ss.mu.Unlock()
	}

	js.mu.Lock()
	js.Completed++
	js.TotalScore += score
	js.TotalIter += iterations
	completed := js.Completed
	total := js.TotalWork
	screens := js.Screens
	js.mu.Unlock()
	o.mu.Unlock()

	// Publish screen.done
	if screenIdx < len(screens) {
		_ = o.publish(ctx, events.ScreenDone, events.ScreenDonePayload{
			JobID:       jobID,
			ScreenIndex: screenIdx,
			ScreenName:  screens[screenIdx].Name,
			Platform:    platform,
			Score:       score,
			Iterations:  iterations,
		})
	}

	// Check if we should start next screen for this platform
	nextIdx := screenIdx + 1
	if nextIdx < len(screens) {
		// Find next incomplete screen for this platform
		nextKey := screenKey{jobID, nextIdx, platform}
		o.mu.RLock()
		nextSS := js.ScreenStates[nextKey]
		o.mu.RUnlock()

		if nextSS != nil && !nextSS.Done {
			return o.requestCodegen(ctx, jobID, nextIdx, platform, screens[nextIdx], nil, 1)
		}
	}

	// All work done?
	if completed >= total {
		return o.completeJob(ctx, jobID)
	}
	return nil
}

func (o *Orchestrator) completeJob(ctx context.Context, jobID string) error {
	o.mu.Lock()
	js := o.jobs[jobID]
	delete(o.jobs, jobID)
	o.mu.Unlock()

	avgScore := 0.0
	totalIter := 0
	platforms := []string{}
	screens := 0

	if js != nil {
		js.mu.Lock()
		if js.Completed > 0 {
			avgScore = js.TotalScore / float64(js.Completed)
		}
		totalIter = js.TotalIter
		platforms = js.Platforms
		screens = len(js.Screens)
		js.mu.Unlock()
	}

	o.emitLog(ctx, jobID, "success", "job_done",
		fmt.Sprintf("🎉 Job complete! %d screens × %d platforms | avg score: %.1f%% | %d total iterations",
			screens, len(platforms), avgScore, totalIter), nil)

	_ = o.store.MarkJobDone(ctx, jobID)

	return o.publish(ctx, events.JobDone, events.JobDonePayload{
		JobID:     jobID,
		Screens:   screens,
		Platforms: platforms,
		AvgScore:  avgScore,
		TotalIter: totalIter,
	})
}

func (o *Orchestrator) publish(ctx context.Context, routingKey string, payload any) error {
	b, err := events.Wrap(routingKey, payload)
	if err != nil {
		return err
	}
	return o.broker.Publish(ctx, routingKey, b)
}

func (o *Orchestrator) emitLog(ctx context.Context, jobID, level, step, message string, data map[string]any) {
	log.Info().Str("job", jobID).Str("step", step).Msg(message)
	p := events.LogEventPayload{
		JobID:   jobID,
		Level:   level,
		Step:    step,
		Message: message,
		Data:    data,
	}
	b, _ := json.Marshal(p)
	// Publish both as log.event and directly to hub
	_ = o.broker.Publish(ctx, events.LogEvent, func() []byte {
		wrapped, _ := events.Wrap(events.LogEvent, p)
		return wrapped
	}())
	o.hub.BroadcastRaw(b)
}

func (o *Orchestrator) killSandbox(ctx context.Context, containerID string) error {
	// publish internal kill message or call docker directly
	// For now just log — sandbox service handles its own cleanup
	log.Debug().Str("container", containerID).Msg("requesting sandbox kill")
	return nil
}

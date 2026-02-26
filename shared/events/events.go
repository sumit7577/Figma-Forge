// Package events defines the message contract published on RabbitMQ.
// Every microservice imports ONLY this package — no direct service-to-service calls.
package events

import (
	"encoding/json"
	"time"

	"github.com/google/uuid"
)

// ── Routing keys (RabbitMQ topic exchange: forge.events) ─────────────────────
const (
	JobSubmitted          = "job.submitted"
	ParseFigmaRequested   = "figma.parse.requested"
	FigmaParsed           = "figma.parsed"
	FigmaFailed           = "figma.failed"
	CodegenRequested      = "codegen.requested"
	CodegenComplete       = "codegen.complete"
	CodegenFailed         = "codegen.failed"
	SandboxBuildRequested = "sandbox.build.requested"
	SandboxReady          = "sandbox.ready"
	SandboxFailed         = "sandbox.failed"
	DiffRequested         = "diff.requested"
	DiffComplete          = "diff.complete"
	DiffFailed            = "diff.failed"
	NotifyRequested       = "notify.requested"
	LogEvent              = "log.event"
	ScreenDone            = "screen.done"
	JobDone               = "job.done"
	JobFailed             = "job.failed"
)

const (
	PlatformReact   = "react"
	PlatformNextJS  = "nextjs"
	PlatformKMP     = "kmp"
	PlatformFlutter = "flutter"
)

// ── Envelope wraps every message ─────────────────────────────────────────────

type Envelope struct {
	ID         string          `json:"id"`
	RoutingKey string          `json:"routing_key"`
	Timestamp  time.Time       `json:"ts"`
	Payload    json.RawMessage `json:"payload"`
}

func Wrap(routingKey string, payload any) ([]byte, error) {
	p, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	return json.Marshal(Envelope{
		ID:         uuid.New().String(),
		RoutingKey: routingKey,
		Timestamp:  time.Now(),
		Payload:    p,
	})
}

func Unwrap[T any](raw []byte) (*T, error) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	var t T
	return &t, json.Unmarshal(env.Payload, &t)
}

func UnwrapEnvelope(raw []byte) (*Envelope, error) {
	var env Envelope
	return &env, json.Unmarshal(raw, &env)
}

// ── Payload types ─────────────────────────────────────────────────────────────

type JobSubmittedPayload struct {
	JobID     string   `json:"job_id"`
	FigmaURL  string   `json:"figma_url"`
	RepoURL   string   `json:"repo_url,omitempty"`
	Platforms []string `json:"platforms"`
	Styling   string   `json:"styling"`
	Threshold int      `json:"threshold"`
}

type TextStyle struct {
	FontFamily    string  `json:"font_family"`
	FontSize      float64 `json:"font_size"`
	FontWeight    int     `json:"font_weight"`
	LineHeight    float64 `json:"line_height"`
	LetterSpacing float64 `json:"letter_spacing"`
}

type ComponentNode struct {
	Type     string          `json:"type"`
	Name     string          `json:"name"`
	Props    map[string]any  `json:"props"`
	Children []ComponentNode `json:"children,omitempty"`
}

type FigmaScreen struct {
	NodeID        string               `json:"node_id"`
	Name          string               `json:"name"`
	Width         float64              `json:"width"`
	Height        float64              `json:"height"`
	Colors        map[string]string    `json:"colors"`
	Typography    map[string]TextStyle `json:"typography"`
	Spacing       []float64            `json:"spacing"`
	BorderRadii   []float64            `json:"border_radii"`
	ComponentTree ComponentNode        `json:"component_tree"`
	ExportURL     string               `json:"export_url"`
}

type FigmaParsedPayload struct {
	JobID       string        `json:"job_id"`
	FileName    string        `json:"file_name"`
	Screens     []FigmaScreen `json:"screens"`
	ScreenCount int           `json:"screen_count"`
}

type FigmaFailedPayload struct {
	JobID string `json:"job_id"`
	Error string `json:"error"`
}

type MismatchRegion struct {
	Property string `json:"property"`
	Actual   string `json:"actual"`
	Expected string `json:"expected"`
	X        int    `json:"x"`
	Y        int    `json:"y"`
	W        int    `json:"w"`
	H        int    `json:"h"`
}

type DiffResult struct {
	Score        float64          `json:"score"`
	Layout       float64          `json:"layout"`
	Typography   float64          `json:"typography"`
	Spacing      float64          `json:"spacing"`
	Color        float64          `json:"color"`
	Regions      []MismatchRegion `json:"regions"`
	DiffImageURL string           `json:"diff_image_url,omitempty"`
}

type CodegenRequestedPayload struct {
	JobID       string      `json:"job_id"`
	ScreenIndex int         `json:"screen_index"`
	Screen      FigmaScreen `json:"screen"`
	Platform    string      `json:"platform"`
	Styling     string      `json:"styling"`
	RepoContext string      `json:"repo_context,omitempty"`
	PrevDiff    *DiffResult `json:"prev_diff,omitempty"`
	Iteration   int         `json:"iteration"`
	Threshold   int         `json:"threshold"`
}

type CodegenCompletePayload struct {
	JobID       string      `json:"job_id"`
	ScreenIndex int         `json:"screen_index"`
	Platform    string      `json:"platform"`
	Iteration   int         `json:"iteration"`
	Code        string      `json:"code"`
	Filename    string      `json:"filename"`
	Threshold   int         `json:"threshold"`
	Screen      FigmaScreen `json:"screen"`
}

type SandboxBuildRequestedPayload struct {
	JobID       string      `json:"job_id"`
	ScreenIndex int         `json:"screen_index"`
	Platform    string      `json:"platform"`
	Iteration   int         `json:"iteration"`
	Code        string      `json:"code"`
	Filename    string      `json:"filename"`
	Threshold   int         `json:"threshold"`
	Screen      FigmaScreen `json:"screen"`
}

type SandboxReadyPayload struct {
	JobID       string      `json:"job_id"`
	ScreenIndex int         `json:"screen_index"`
	Platform    string      `json:"platform"`
	Iteration   int         `json:"iteration"`
	ContainerID string      `json:"container_id"`
	Port        int         `json:"port"`
	URL         string      `json:"url"`
	Threshold   int         `json:"threshold"`
	Screen      FigmaScreen `json:"screen"`
}

type SandboxFailedPayload struct {
	JobID       string `json:"job_id"`
	ScreenIndex int    `json:"screen_index"`
	Platform    string `json:"platform"`
	Error       string `json:"error"`
	BuildLog    string `json:"build_log"`
}

type DiffRequestedPayload struct {
	JobID          string      `json:"job_id"`
	ScreenIndex    int         `json:"screen_index"`
	Platform       string      `json:"platform"`
	Iteration      int         `json:"iteration"`
	SandboxURL     string      `json:"sandbox_url"`
	ContainerID    string      `json:"container_id"`
	FigmaExportURL string      `json:"figma_export_url"`
	Screen         FigmaScreen `json:"screen"`
	Threshold      int         `json:"threshold"`
}

type DiffCompletePayload struct {
	JobID       string      `json:"job_id"`
	ScreenIndex int         `json:"screen_index"`
	Platform    string      `json:"platform"`
	Iteration   int         `json:"iteration"`
	ContainerID string      `json:"container_id"`
	Diff        DiffResult  `json:"diff"`
	Threshold   int         `json:"threshold"`
	Passed      bool        `json:"passed"`
	Screen      FigmaScreen `json:"screen"`
}

type DiffFailedPayload struct {
	JobID       string `json:"job_id"`
	ScreenIndex int    `json:"screen_index"`
	Platform    string `json:"platform"`
	Error       string `json:"error"`
}

type NotifyRequestedPayload struct {
	JobID        string  `json:"job_id"`
	ScreenName   string  `json:"screen_name"`
	Platform     string  `json:"platform"`
	Score        float64 `json:"score"`
	Iterations   int     `json:"iterations"`
	DiffImageURL string  `json:"diff_image_url"`
}

type LogEventPayload struct {
	JobID   string         `json:"job_id"`
	Level   string         `json:"level"`
	Step    string         `json:"step"`
	Message string         `json:"message"`
	Data    map[string]any `json:"data,omitempty"`
}

type ScreenDonePayload struct {
	JobID       string  `json:"job_id"`
	ScreenIndex int     `json:"screen_index"`
	ScreenName  string  `json:"screen_name"`
	Platform    string  `json:"platform"`
	Score       float64 `json:"score"`
	Iterations  int     `json:"iterations"`
}

type JobDonePayload struct {
	JobID     string   `json:"job_id"`
	Screens   int      `json:"screens"`
	Platforms []string `json:"platforms"`
	AvgScore  float64  `json:"avg_score"`
	TotalIter int      `json:"total_iterations"`
}

type JobFailedPayload struct {
	JobID string `json:"job_id"`
	Error string `json:"error"`
	Step  string `json:"step"`
}

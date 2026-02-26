package internal

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"time"

	"github.com/forge-ai/forge/shared/events"
)

type Store struct {
	url    string
	key    string
	client *http.Client
}

func NewStore(url, key string) *Store {
	return &Store{url: url, key: key, client: &http.Client{Timeout: 10 * time.Second}}
}

func (s *Store) CreateJob(ctx context.Context, p *events.JobSubmittedPayload) error {
	if s.url == "" { return nil }
	return s.post(ctx, "jobs", map[string]any{
		"id":        p.JobID,
		"figma_url": p.FigmaURL,
		"repo_url":  p.RepoURL,
		"platforms": p.Platforms,
		"styling":   p.Styling,
		"threshold": p.Threshold,
		"status":    "pending",
	})
}

func (s *Store) UpdateJobScreenCount(ctx context.Context, jobID string, count int) error {
	if s.url == "" { return nil }
	return s.patch(ctx, "jobs?id=eq."+jobID, map[string]any{
		"screen_count": count,
		"status":       "running",
		"updated_at":   time.Now(),
	})
}

func (s *Store) MarkJobDone(ctx context.Context, jobID string) error {
	if s.url == "" { return nil }
	return s.patch(ctx, "jobs?id=eq."+jobID, map[string]any{
		"status": "done", "updated_at": time.Now(),
	})
}

func (s *Store) MarkJobFailed(ctx context.Context, jobID, errMsg string) error {
	if s.url == "" { return nil }
	return s.patch(ctx, "jobs?id=eq."+jobID, map[string]any{
		"status": "failed", "error": errMsg, "updated_at": time.Now(),
	})
}

func (s *Store) SaveIteration(ctx context.Context, p events.DiffCompletePayload) error {
	if s.url == "" { return nil }
	return s.post(ctx, "iterations", map[string]any{
		"job_id":          p.JobID,
		"screen_name":     p.Screen.Name,
		"platform":        p.Platform,
		"iteration":       p.Iteration,
		"score":           p.Diff.Score,
		"layout_score":    p.Diff.Layout,
		"typo_score":      p.Diff.Typography,
		"spacing_score":   p.Diff.Spacing,
		"color_score":     p.Diff.Color,
		"diff_url":        p.Diff.DiffImageURL,
		"mismatch_regions": p.Diff.Regions,
	})
}

func (s *Store) post(ctx context.Context, table string, v any) error {
	b, _ := json.Marshal(v)
	req, _ := http.NewRequestWithContext(ctx, "POST", s.url+"/rest/v1/"+table, bytes.NewReader(b))
	s.headers(req)
	req.Header.Set("Prefer", "return=minimal")
	resp, err := s.client.Do(req)
	if err != nil { return err }
	defer resp.Body.Close()
	if resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return fmt.Errorf("supabase %d: %s", resp.StatusCode, raw)
	}
	return nil
}

func (s *Store) patch(ctx context.Context, path string, v any) error {
	b, _ := json.Marshal(v)
	req, _ := http.NewRequestWithContext(ctx, "PATCH", s.url+"/rest/v1/"+path, bytes.NewReader(b))
	s.headers(req)
	resp, err := s.client.Do(req)
	if err != nil { return err }
	defer resp.Body.Close()
	return nil
}

func (s *Store) headers(req *http.Request) {
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+s.key)
	req.Header.Set("apikey", s.key)
}

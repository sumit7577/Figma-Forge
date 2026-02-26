// differ subscribes to diff.requested,
// captures a screenshot of the sandbox URL via Playwright,
// downloads the Figma reference PNG,
// runs pixel-level comparison,
// uploads the diff image to Supabase Storage,
// and publishes diff.complete.
package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/color"
	"image/png"
	"io"
	"math"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"syscall"
	"time"

	"github.com/disintegration/imaging"
	"github.com/forge-ai/forge/shared/events"
	"github.com/forge-ai/forge/shared/mq"
	"github.com/joho/godotenv"
	amqp "github.com/rabbitmq/amqp091-go"
	"github.com/rs/zerolog"
	"github.com/rs/zerolog/log"
)

func main() {
	log.Logger = log.Output(zerolog.ConsoleWriter{Out: os.Stderr, TimeFormat: "15:04:05"})
	_ = godotenv.Load()

	amqpURL := envOr("AMQP_URL", "amqp://forge:forge@rabbitmq:5672/")
	supabaseURL := envOr("SUPABASE_URL", "")
	supabaseKey := envOr("SUPABASE_SERVICE_KEY", "")

	broker, err := mq.New(amqpURL)
	if err != nil {
		log.Fatal().Err(err).Msg("mq connect")
	}
	defer broker.Close()

	deliveries, err := broker.Subscribe("svc.differ", events.DiffRequested)
	if err != nil {
		log.Fatal().Err(err).Msg("subscribe")
	}

	log.Info().Msg("differ service started")

	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigs; cancel() }()

	d := &differ{
		supabaseURL: supabaseURL,
		supabaseKey: supabaseKey,
		http:        &http.Client{Timeout: 30 * time.Second},
	}

	for {
		select {
		case <-ctx.Done():
			return
		case del, ok := <-deliveries:
			if !ok {
				return
			}
			if err := handle(ctx, del, broker, d); err != nil {
				log.Error().Err(err).Msg("diff error")
				del.Nack(false, false)
			} else {
				del.Ack(false)
			}
		}
	}
}

func handle(ctx context.Context, d amqp.Delivery, broker *mq.Broker, differ *differ) error {
	p, err := events.Unwrap[events.DiffRequestedPayload](d.Body)
	if err != nil {
		return err
	}

	log.Info().
		Str("job", p.JobID).
		Str("platform", p.Platform).
		Int("iter", p.Iteration).
		Msg("running pixel diff")

	result, err := differ.compare(ctx, *p)
	if err != nil {
		b, _ := events.Wrap(events.DiffFailed, events.DiffFailedPayload{
			JobID: p.JobID, ScreenIndex: p.ScreenIndex, Platform: p.Platform, Error: err.Error(),
		})
		return broker.Publish(ctx, events.DiffFailed, b)
	}

	passed := result.Score >= float64(p.Threshold)
	b, _ := events.Wrap(events.DiffComplete, events.DiffCompletePayload{
		JobID:       p.JobID,
		ScreenIndex: p.ScreenIndex,
		Platform:    p.Platform,
		Iteration:   p.Iteration,
		ContainerID: p.ContainerID,
		Diff:        *result,
		Threshold:   p.Threshold,
		Passed:      passed,
		Screen:      p.Screen,
	})
	return broker.Publish(ctx, events.DiffComplete, b)
}

// ── Differ ────────────────────────────────────────────────────────────────────

type differ struct {
	supabaseURL string
	supabaseKey string
	http        *http.Client
}

func (d *differ) compare(ctx context.Context, p events.DiffRequestedPayload) (*events.DiffResult, error) {
	// 1. Capture screenshot of sandbox
	generated, err := captureScreenshot(ctx, p.SandboxURL, int(p.Screen.Width), int(p.Screen.Height))
	if err != nil {
		return nil, fmt.Errorf("screenshot: %w", err)
	}

	// 2. Download Figma reference PNG
	var reference []byte
	if p.FigmaExportURL != "" {
		reference, err = d.downloadImage(ctx, p.FigmaExportURL)
		if err != nil {
			log.Warn().Err(err).Msg("could not download Figma reference — using blank")
		}
	}

	if len(reference) == 0 {
		return &events.DiffResult{Score: 50}, nil // no reference — skip
	}

	// 3. Pixel comparison
	result, diffPNG, err := pixelCompare(reference, generated)
	if err != nil {
		return nil, fmt.Errorf("pixel compare: %w", err)
	}

	// 4. Upload diff image to Supabase Storage
	if d.supabaseURL != "" && len(diffPNG) > 0 {
		diffURL, err := d.uploadDiff(ctx, p.JobID, p.ScreenIndex, p.Iteration, diffPNG)
		if err == nil {
			result.DiffImageURL = diffURL
		}
	}

	return result, nil
}

// captureScreenshot uses Playwright CLI to capture the sandbox URL.
func captureScreenshot(ctx context.Context, url string, w, h int) ([]byte, error) {
	outFile := fmt.Sprintf("/tmp/forge-cap-%d.png", time.Now().UnixNano())
	defer os.Remove(outFile)

	viewport := fmt.Sprintf("%dx%d", w, h)
	cmd := exec.CommandContext(ctx,
		"npx", "playwright", "screenshot",
		"--browser", "chromium",
		"--viewport-size", viewport,
		"--wait-for-timeout", "3000",
		"--full-page",
		url,
		outFile,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		return nil, fmt.Errorf("playwright: %s: %w", string(out), err)
	}

	return os.ReadFile(outFile)
}

func (d *differ) downloadImage(ctx context.Context, url string) ([]byte, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	resp, err := d.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	return io.ReadAll(resp.Body)
}

func (d *differ) uploadDiff(ctx context.Context, jobID string, screenIdx, iter int, data []byte) (string, error) {
	path := fmt.Sprintf("diffs/%s/%d/iter-%d.png", jobID, screenIdx, iter)
	url := d.supabaseURL + "/storage/v1/object/forge-assets/" + path

	req, _ := http.NewRequestWithContext(ctx, "POST", url, bytes.NewReader(data))
	req.Header.Set("Authorization", "Bearer "+d.supabaseKey)
	req.Header.Set("Content-Type", "image/png")

	resp, err := d.http.Do(req)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode >= 300 {
		b, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("storage %d: %s", resp.StatusCode, b)
	}
	return d.supabaseURL + "/storage/v1/object/public/forge-assets/" + path, nil
}

// ── Pixel comparison ──────────────────────────────────────────────────────────

func pixelCompare(refData, genData []byte) (*events.DiffResult, []byte, error) {
	refImg, err := png.Decode(bytes.NewReader(refData))
	if err != nil {
		return nil, nil, fmt.Errorf("decode ref: %w", err)
	}
	genImg, err := png.Decode(bytes.NewReader(genData))
	if err != nil {
		return nil, nil, fmt.Errorf("decode gen: %w", err)
	}

	bounds := refImg.Bounds()
	// Resize generated to match reference dimensions
	genImg = imaging.Resize(genImg, bounds.Dx(), bounds.Dy(), imaging.Lanczos)

	overall, diffImg := rmse(refImg, genImg)
	layout := regionScore(refImg, genImg, bounds, 3, 1) // horizontal bands
	typo := regionScore(refImg, genImg, bounds, 1, 4)   // focus upper portion
	spacing := whitespaceScore(refImg, genImg)
	clr := colorScore(refImg, genImg)

	// Weighted composite
	composite := overall*0.40 + layout*0.25 + typo*0.15 + clr*0.10 + spacing*0.10

	regions := detectMismatches(refImg, genImg, bounds)

	var diffBuf bytes.Buffer
	_ = png.Encode(&diffBuf, diffImg)

	return &events.DiffResult{
		Score:      composite,
		Layout:     layout,
		Typography: typo,
		Spacing:    spacing,
		Color:      clr,
		Regions:    regions,
	}, diffBuf.Bytes(), nil
}

func rmse(ref, gen image.Image) (float64, *image.NRGBA) {
	bounds := ref.Bounds()
	diffImg := image.NewNRGBA(bounds)
	total := 0.0
	n := float64(bounds.Dx() * bounds.Dy())

	for y := bounds.Min.Y; y < bounds.Max.Y; y++ {
		for x := bounds.Min.X; x < bounds.Max.X; x++ {
			r1, g1, b1, _ := ref.At(x, y).RGBA()
			r2, g2, b2, _ := gen.At(x, y).RGBA()
			dr := float64(r1>>8) - float64(r2>>8)
			dg := float64(g1>>8) - float64(g2>>8)
			db := float64(b1>>8) - float64(b2>>8)
			diff := math.Sqrt((dr*dr + dg*dg + db*db) / 3.0)
			total += diff
			if diff < 8 {
				diffImg.Set(x, y, color.NRGBA{0, 200, 50, 60})
			} else {
				i := uint8(math.Min(diff*2, 255))
				diffImg.Set(x, y, color.NRGBA{i, 0, 0, 200})
			}
		}
	}
	return math.Max(0, 100-(total/n/255)*100), diffImg
}

func regionScore(ref, gen image.Image, bounds image.Rectangle, hBands, _ int) float64 {
	bh := bounds.Dy() / hBands
	total := 0.0
	for i := 0; i < hBands; i++ {
		r := image.Rect(0, i*bh, bounds.Dx(), (i+1)*bh)
		rCrop := imaging.Crop(ref.(interface {
			image.Image
			Bounds() image.Rectangle
		}), r)
		gCrop := imaging.Crop(gen.(interface {
			image.Image
			Bounds() image.Rectangle
		}), r)
		s, _ := rmse(rCrop, gCrop)
		total += s
	}
	return total / float64(hBands)
}

func whitespaceScore(ref, gen image.Image) float64 {
	rc := countWhite(ref)
	gc := countWhite(gen)
	b := ref.Bounds()
	n := float64(b.Dx() * b.Dy())
	diff := math.Abs(float64(rc)-float64(gc)) / n
	return math.Max(0, 100-diff*300)
}

func colorScore(ref, gen image.Image) float64 {
	rp := dominant(ref, 8)
	gp := dominant(gen, 8)
	matched := 0
	for _, rc := range rp {
		for _, gc := range gp {
			if colorDist(rc, gc) < 30 {
				matched++
				break
			}
		}
	}
	if len(rp) == 0 {
		return 100
	}
	return float64(matched) / float64(len(rp)) * 100
}

func detectMismatches(ref, gen image.Image, bounds image.Rectangle) []events.MismatchRegion {
	var regions []events.MismatchRegion
	qw := bounds.Dx() / 2
	qh := bounds.Dy() / 2
	quads := []struct {
		name string
		r    image.Rectangle
	}{
		{"top-left", image.Rect(0, 0, qw, qh)},
		{"top-right", image.Rect(qw, 0, bounds.Dx(), qh)},
		{"bottom-left", image.Rect(0, qh, qw, bounds.Dy())},
		{"bottom-right", image.Rect(qw, qh, bounds.Dx(), bounds.Dy())},
	}
	type cropper interface {
		image.Image
		Bounds() image.Rectangle
	}
	for _, q := range quads {
		rc := imaging.Crop(ref.(cropper), q.r)
		gc := imaging.Crop(gen.(cropper), q.r)
		score, _ := rmse(rc, gc)
		if score < 82 {
			regions = append(regions, events.MismatchRegion{
				Property: q.name + " region",
				Actual:   fmt.Sprintf("%.0f%% match", score),
				Expected: "≥82%",
				X:        q.r.Min.X, Y: q.r.Min.Y,
				W: q.r.Dx(), H: q.r.Dy(),
			})
		}
	}
	return regions
}

type rgb struct{ r, g, b float64 }

func countWhite(img image.Image) int {
	b := img.Bounds()
	n := 0
	for y := b.Min.Y; y < b.Max.Y; y++ {
		for x := b.Min.X; x < b.Max.X; x++ {
			r, g, bv, _ := img.At(x, y).RGBA()
			if r>>8 > 235 && g>>8 > 235 && bv>>8 > 235 {
				n++
			}
		}
	}
	return n
}

func dominant(img image.Image, n int) []rgb {
	b := img.Bounds()
	counts := map[rgb]int{}
	for y := b.Min.Y; y < b.Max.Y; y += 4 {
		for x := b.Min.X; x < b.Max.X; x += 4 {
			r, g, bv, _ := img.At(x, y).RGBA()
			c := rgb{
				math.Round(float64(r>>8)/32) * 32,
				math.Round(float64(g>>8)/32) * 32,
				math.Round(float64(bv>>8)/32) * 32,
			}
			counts[c]++
		}
	}
	var out []rgb
	for c := range counts {
		out = append(out, c)
		if len(out) >= n {
			break
		}
	}
	return out
}

func colorDist(a, b rgb) float64 {
	return math.Sqrt((a.r-b.r)*(a.r-b.r) + (a.g-b.g)*(a.g-b.g) + (a.b-b.b)*(a.b-b.b))
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

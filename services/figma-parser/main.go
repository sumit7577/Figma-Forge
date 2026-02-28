// figma-parser subscribes to figma.parse.requested,
// calls the Figma REST API, extracts screens + design tokens,
// and publishes figma.parsed.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"regexp"
	"strings"
	"syscall"

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
	figmaToken := mustEnv("FIGMA_TOKEN")

	broker, err := mq.New(amqpURL)
	if err != nil {
		log.Fatal().Err(err).Msg("mq connect failed")
	}
	defer broker.Close()

	deliveries, err := broker.Subscribe("svc.figma.parser", events.ParseFigmaRequested)
	if err != nil {
		log.Fatal().Err(err).Msg("subscribe failed")
	}

	log.Info().Msg("figma-parser service started")

	ctx, cancel := context.WithCancel(context.Background())
	sigs := make(chan os.Signal, 1)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM)
	go func() { <-sigs; cancel() }()

	client := &figmaClient{token: figmaToken, http: &http.Client{}}

	for {
		select {
		case <-ctx.Done():
			return
		case d, ok := <-deliveries:
			if !ok {
				return
			}
			if err := handle(ctx, d, broker, client); err != nil {
				log.Error().Err(err).Msg("figma parse failed")
				d.Nack(false, false)
			} else {
				d.Ack(false)
			}
		}
	}
}

func handle(ctx context.Context, d amqp.Delivery, broker *mq.Broker, client *figmaClient) error {
	p, err := events.Unwrap[events.ParseFigmaRequestedPayload](d.Body)
	if err != nil {
		return err
	}

	log.Info().Str("job", p.JobID).Str("url", p.FigmaURL).Msg("parsing Figma file")

	file, err := client.parseFile(ctx, p.FigmaURL)
	if err != nil {
		b, _ := events.Wrap(events.FigmaFailed, events.FigmaFailedPayload{
			JobID: p.JobID,
			Error: err.Error(),
		})
		return broker.Publish(ctx, events.FigmaFailed, b)
	}

	b, _ := events.Wrap(events.FigmaParsed, events.FigmaParsedPayload{
		JobID:       p.JobID,
		FileName:    file.Name,
		Screens:     file.Screens,
		ScreenCount: len(file.Screens),
	})
	return broker.Publish(ctx, events.FigmaParsed, b)
}

// ── Figma API client ─────────────────────────────────────────────────────────

const figmaBase = "https://api.figma.com/v1"

type figmaClient struct {
	token string
	http  *http.Client
}

type parsedFile struct {
	Name    string
	Screens []events.FigmaScreen
}

func (c *figmaClient) parseFile(ctx context.Context, fileURL string) (*parsedFile, error) {
	key, err := extractKey(fileURL)
	if err != nil {
		return nil, err
	}

	doc, name, err := c.getFile(ctx, key)
	if err != nil {
		return nil, err
	}

	screens := extractScreens(doc)

	// Export all screens as PNG
	if len(screens) > 0 {
		nodeIDs := make([]string, len(screens))
		for i, s := range screens {
			nodeIDs[i] = s.NodeID
		}
		urls, err := c.exportImages(ctx, key, nodeIDs)
		if err != nil {
			log.Warn().Err(err).Msg("failed to export screen images")
		} else {
			for i := range screens {
				if u, ok := urls[screens[i].NodeID]; ok {
					screens[i].ExportURL = u
				}
			}
			log.Info().Int("count", len(screens)).Msg("exported screen images")
		}
	}

	return &parsedFile{Name: name, Screens: screens}, nil
}

type figmaNode struct {
	ID   string `json:"id"`
	Name string `json:"name"`
	Type string `json:"type"`
	Children []figmaNode `json:"children"`
	AbsoluteBoundingBox *struct {
		Width  float64 `json:"width"`
		Height float64 `json:"height"`
	} `json:"absoluteBoundingBox"`
	Fills []struct {
		Type  string `json:"type"`
		Color *struct{ R, G, B, A float64 } `json:"color"`
	} `json:"fills"`
	Style *struct {
		FontFamily    string  `json:"fontFamily"`
		FontSize      float64 `json:"fontSize"`
		FontWeight    int     `json:"fontWeight"`
		LineHeightPx  float64 `json:"lineHeightPx"`
		LetterSpacing float64 `json:"letterSpacing"`
	} `json:"style"`
	PaddingTop    float64 `json:"paddingTop"`
	PaddingRight  float64 `json:"paddingRight"`
	PaddingBottom float64 `json:"paddingBottom"`
	PaddingLeft   float64 `json:"paddingLeft"`
	ItemSpacing   float64 `json:"itemSpacing"`
	CornerRadius  float64 `json:"cornerRadius"`
}

func (c *figmaClient) getFile(ctx context.Context, key string) ([]figmaNode, string, error) {
	req, _ := http.NewRequestWithContext(ctx, "GET", figmaBase+"/files/"+key, nil)
	req.Header.Set("X-Figma-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, "", err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		b, _ := io.ReadAll(resp.Body)
		return nil, "", fmt.Errorf("figma API %d: %s", resp.StatusCode, b)
	}
	var result struct {
		Name     string `json:"name"`
		Document struct {
			Children []figmaNode `json:"children"`
		} `json:"document"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, "", err
	}
	return result.Document.Children, result.Name, nil
}

func (c *figmaClient) exportImages(ctx context.Context, key string, nodeIDs []string) (map[string]string, error) {
	ids := strings.Join(nodeIDs, ",")
	url := fmt.Sprintf("%s/images/%s?ids=%s&format=png&scale=2", figmaBase, key, ids)
	req, _ := http.NewRequestWithContext(ctx, "GET", url, nil)
	req.Header.Set("X-Figma-Token", c.token)
	resp, err := c.http.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("figma export API %d: %s", resp.StatusCode, b)
	}
	var result struct {
		Images map[string]string `json:"images"`
	}
	json.NewDecoder(resp.Body).Decode(&result)
	return result.Images, nil
}

var keyRe = regexp.MustCompile(`figma\.com/(?:file|design)/([A-Za-z0-9]+)`)

func extractKey(url string) (string, error) {
	m := keyRe.FindStringSubmatch(url)
	if len(m) < 2 {
		return "", fmt.Errorf("invalid Figma URL: %q", url)
	}
	return m[1], nil
}

func extractScreens(pages []figmaNode) []events.FigmaScreen {
	var screens []events.FigmaScreen
	for _, page := range pages {
		if page.Type != "CANVAS" {
			continue
		}
		for _, node := range page.Children {
			if node.Type != "FRAME" {
				continue
			}
			s := events.FigmaScreen{
				NodeID:     node.ID,
				Name:       node.Name,
				Colors:     make(map[string]string),
				Typography: make(map[string]events.TextStyle),
			}
			if node.AbsoluteBoundingBox != nil {
				s.Width = node.AbsoluteBoundingBox.Width
				s.Height = node.AbsoluteBoundingBox.Height
			}
			walkTokens(node, &s)
			s.ComponentTree = toComponent(node)
			screens = append(screens, s)
		}
	}
	return screens
}

func walkTokens(node figmaNode, s *events.FigmaScreen) {
	for _, f := range node.Fills {
		if f.Type == "SOLID" && f.Color != nil {
			hex := fmt.Sprintf("#%02X%02X%02X",
				int(f.Color.R*255), int(f.Color.G*255), int(f.Color.B*255))
			s.Colors[node.Name] = hex
		}
	}
	if node.Style != nil {
		s.Typography[node.Name] = events.TextStyle{
			FontFamily:    node.Style.FontFamily,
			FontSize:      node.Style.FontSize,
			FontWeight:    node.Style.FontWeight,
			LineHeight:    node.Style.LineHeightPx,
			LetterSpacing: node.Style.LetterSpacing,
		}
	}
	if node.CornerRadius > 0 {
		s.BorderRadii = appendUniq(s.BorderRadii, node.CornerRadius)
	}
	if node.ItemSpacing > 0 {
		s.Spacing = appendUniq(s.Spacing, node.ItemSpacing)
	}
	for _, child := range node.Children {
		walkTokens(child, s)
	}
}

func toComponent(node figmaNode) events.ComponentNode {
	cn := events.ComponentNode{
		Type: node.Type,
		Name: node.Name,
		Props: map[string]any{
			"padding": [4]float64{node.PaddingTop, node.PaddingRight, node.PaddingBottom, node.PaddingLeft},
			"gap":     node.ItemSpacing,
			"radius":  node.CornerRadius,
		},
	}
	for _, child := range node.Children {
		cn.Children = append(cn.Children, toComponent(child))
	}
	return cn
}

func appendUniq(sl []float64, v float64) []float64 {
	for _, x := range sl {
		if x == v {
			return sl
		}
	}
	return append(sl, v)
}

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func mustEnv(k string) string {
	v := os.Getenv(k)
	if v == "" {
		log.Fatal().Str("key", k).Msg("required env var missing")
	}
	return v
}

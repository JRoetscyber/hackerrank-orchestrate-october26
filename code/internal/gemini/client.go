// Package gemini is a minimal REST client for the Gemini generateContent API.
// It uses only net/http so the binary stays small and dependency-free.
package gemini

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"os"
	"strconv"
	"strings"
	"time"
)

const defaultBase = "https://generativelanguage.googleapis.com/v1beta"

// Client talks to Gemini. Configure it with FromEnv.
type Client struct {
	APIKey  string
	Model   string
	BaseURL string
	HTTP    *http.Client
	// Prices are USD per one million tokens, used only for cost estimates.
	PriceInPerM  float64
	PriceOutPerM float64
}

// Usage is the token accounting returned by one call.
type Usage struct {
	InputTokens  int
	OutputTokens int // candidates + thinking tokens (both are billed as output)
	Latency      time.Duration
	CostUSD      float64
}

// FromEnv builds a client from GEMINI_API_KEY and optional GEMINI_VISION_MODEL / GEMINI_MODEL.
func FromEnv(modelEnvKeys ...string) (*Client, error) {
	key := os.Getenv("GEMINI_API_KEY")
	if key == "" {
		return nil, errors.New("GEMINI_API_KEY is not set (put it in .env)")
	}
	model := ""
	for _, k := range modelEnvKeys {
		if v := os.Getenv(k); v != "" {
			model = v
			break
		}
	}
	if model == "" {
		model = "gemini-3.5-flash-lite"
	}
	base := os.Getenv("GEMINI_BASE_URL")
	if base == "" {
		base = defaultBase
	}
	return &Client{
		APIKey:       key,
		Model:        model,
		BaseURL:      strings.TrimRight(base, "/"),
		HTTP:         &http.Client{Timeout: 180 * time.Second},
		PriceInPerM:  envFloat("GEMINI_PRICE_INPUT_PER_M", 0.30),
		PriceOutPerM: envFloat("GEMINI_PRICE_OUTPUT_PER_M", 2.50),
	}, nil
}

func envFloat(k string, def float64) float64 {
	if v, err := strconv.ParseFloat(os.Getenv(k), 64); err == nil {
		return v
	}
	return def
}

// File is an inline binary attachment (image or PDF).
type File struct {
	MIME string
	Data []byte
}

// GenerateJSON sends prompt + files and forces a JSON response matching schema.
// It returns the raw JSON text from the model.
func (c *Client) GenerateJSON(ctx context.Context, prompt string, files []File, schema map[string]any) (string, Usage, error) {
	parts := []map[string]any{}
	for _, f := range files {
		parts = append(parts, map[string]any{"inlineData": map[string]any{
			"mimeType": f.MIME, "data": base64.StdEncoding.EncodeToString(f.Data),
		}})
	}
	parts = append(parts, map[string]any{"text": prompt})
	body, _ := json.Marshal(map[string]any{
		"contents": []any{map[string]any{"role": "user", "parts": parts}},
		"generationConfig": map[string]any{
			"temperature":      0,
			"responseMimeType": "application/json",
			"responseSchema":   schema,
		},
	})
	url := fmt.Sprintf("%s/models/%s:generateContent", c.BaseURL, c.Model)

	var lastErr error
	for attempt := 0; attempt < 6; attempt++ {
		if attempt > 0 {
			wait := time.Duration(1<<attempt)*time.Second + time.Duration(rand.Intn(750))*time.Millisecond
			select {
			case <-ctx.Done():
				return "", Usage{}, ctx.Err()
			case <-time.After(wait):
			}
		}
		start := time.Now()
		req, _ := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("x-goog-api-key", c.APIKey) // header, not query string, so the key never lands in logs/URLs
		resp, err := c.HTTP.Do(req)
		if err != nil {
			lastErr = err
			continue
		}
		raw, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		if resp.StatusCode == 429 || resp.StatusCode >= 500 {
			lastErr = fmt.Errorf("gemini http %d: %s", resp.StatusCode, trunc(raw))
			continue
		}
		if resp.StatusCode != 200 {
			return "", Usage{}, fmt.Errorf("gemini http %d: %s", resp.StatusCode, trunc(raw))
		}
		var out struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text string `json:"text"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			Usage struct {
				Prompt     int `json:"promptTokenCount"`
				Candidates int `json:"candidatesTokenCount"`
				Thoughts   int `json:"thoughtsTokenCount"`
			} `json:"usageMetadata"`
		}
		if err := json.Unmarshal(raw, &out); err != nil {
			return "", Usage{}, fmt.Errorf("decode gemini response: %w", err)
		}
		if len(out.Candidates) == 0 || len(out.Candidates[0].Content.Parts) == 0 {
			lastErr = fmt.Errorf("gemini returned no content: %s", trunc(raw))
			continue
		}
		var sb strings.Builder
		for _, p := range out.Candidates[0].Content.Parts {
			sb.WriteString(p.Text)
		}
		u := Usage{
			InputTokens:  out.Usage.Prompt,
			OutputTokens: out.Usage.Candidates + out.Usage.Thoughts,
			Latency:      time.Since(start),
		}
		u.CostUSD = float64(u.InputTokens)/1e6*c.PriceInPerM + float64(u.OutputTokens)/1e6*c.PriceOutPerM
		return sb.String(), u, nil
	}
	return "", Usage{}, fmt.Errorf("gemini failed after retries: %w", lastErr)
}

func trunc(b []byte) string {
	s := string(b)
	if len(s) > 400 {
		s = s[:400] + "..."
	}
	return s
}

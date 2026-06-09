// Package translate is a thin client for a self-hosted LibreTranslate instance
// (open-source MT, no paid API). Used to translate reporter damage descriptions
// into the analysts' common language. Best-effort: any failure leaves the original.
package translate

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"time"
)

type Client struct {
	url    string
	target string
	http   *http.Client
}

func New(url, target string) *Client {
	if target == "" {
		target = "en"
	}
	return &Client{url: url, target: target, http: &http.Client{Timeout: 6 * time.Second}}
}

func (c *Client) Enabled() bool { return c != nil && c.url != "" }
func (c *Client) Target() string { return c.target }

type ltReq struct {
	Q      string `json:"q"`
	Source string `json:"source"`
	Target string `json:"target"`
	Format string `json:"format"`
}
type ltResp struct {
	TranslatedText   string `json:"translatedText"`
	DetectedLanguage struct {
		Language   string  `json:"language"`
		Confidence float64 `json:"confidence"`
	} `json:"detectedLanguage"`
}

// Translate returns (translatedText, detectedSourceLang, ok). Source language is
// auto-detected. Returns ok=false on any error (caller keeps the original verbatim).
func (c *Client) Translate(ctx context.Context, text string) (translated, detectedLang string, ok bool) {
	if !c.Enabled() || text == "" {
		return "", "", false
	}
	body, _ := json.Marshal(ltReq{Q: text, Source: "auto", Target: c.target, Format: "text"})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.url+"/translate", bytes.NewReader(body))
	if err != nil {
		return "", "", false
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return "", "", false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return "", "", false
	}
	var r ltResp
	if json.NewDecoder(resp.Body).Decode(&r) != nil {
		return "", "", false
	}
	return r.TranslatedText, r.DetectedLanguage.Language, r.TranslatedText != ""
}

// Package clef maps torrent files the rules cannot map with certainty (internal/meta,
// route.go) using Cloudflare's decision model Clef (Workers AI @cf/cloudflare/clef-flash or
// @cf/cloudflare/clef). Clef outputs no text, only a probability for every option of every
// question we ask, so every answer is one of our own option ids (candidate subjects, number
// tokens of the file name, kinds, or "none"), validated against the official episode list,
// and applied / queued for review / discarded by calibrated thresholds
// (docs/research/clef-calibration.md).
package clef

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// DefaultBaseURL is the Cloudflare API base (overridable in settings, e.g. for a local fake).
const DefaultBaseURL = "https://api.cloudflare.com/client/v4"

// Models.
const (
	ModelFlash = "clef-flash"
	ModelFull  = "clef"
)

// Question is one typed question (only "choice" is used).
type Question struct {
	Type         string `json:"type"`
	Instructions any    `json:"instructions"`
	Criteria     any    `json:"criteria,omitempty"`
}

// Request is the Workers AI input (schema-input.json).
type Request struct {
	Model     string              `json:"model"`
	State     any                 `json:"state"`
	Questions map[string]Question `json:"questions"`
}

// Answer is one answer (choice).
type Answer struct {
	Type          string             `json:"type"`
	Choice        string             `json:"choice"`
	Probabilities map[string]float64 `json:"probabilities"`
	Confidence    float64            `json:"confidence"`
	Noul          *float64           `json:"noul,omitempty"` // yes/no questions: probability of yes
}

// Usage is the token usage of a request.
type Usage struct {
	InputTokens  int `json:"input_tokens"`
	OutputTokens int `json:"output_tokens"`
}

// Response is the Workers AI result (schema-output.json).
type Response struct {
	Model   string            `json:"model"`
	Answers map[string]Answer `json:"answers"`
	Usage   Usage             `json:"usage"`
}

// Client runs one request.
type Client interface {
	Run(ctx context.Context, req Request) (*Response, error)
}

// HTTPClient calls POST {base}/accounts/{account}/ai/run/@cf/cloudflare/{model}.
type HTTPClient struct {
	BaseURL   string
	AccountID string
	Token     string
	HTTP      *http.Client
}

// APIError is a non-2xx answer; Retry tells whether retrying later may help.
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string { return fmt.Sprintf("clef: HTTP %d: %s", e.Status, e.Message) }

// QuotaExhausted: the account's daily Workers AI allocation is used up (free plan: error
// 4006 "you have used up your daily free allocation of 10,000 neurons"); retried the next
// UTC day.
func QuotaExhausted(err error) bool {
	var ae *APIError
	return errors.As(err, &ae) && (strings.Contains(ae.Message, "4006") || strings.Contains(ae.Message, "daily free allocation"))
}

// Retryable reports whether an error is worth retrying (network, 429, 5xx).
func Retryable(err error) bool {
	var ae *APIError
	if errors.As(err, &ae) {
		return ae.Status == 429 || ae.Status >= 500
	}
	return !errors.Is(err, context.Canceled)
}

func (c *HTTPClient) Run(ctx context.Context, req Request) (*Response, error) {
	base := strings.TrimRight(c.BaseURL, "/")
	if base == "" {
		base = DefaultBaseURL
	}
	if c.AccountID == "" || c.Token == "" {
		return nil, &APIError{Status: 400, Message: "account id / token not set"}
	}
	model := strings.TrimSpace(req.Model)
	if model != ModelFlash && model != ModelFull {
		return nil, &APIError{Status: 400, Message: "unknown model " + model}
	}
	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	u := base + "/accounts/" + c.AccountID + "/ai/run/@cf/cloudflare/" + model
	hr, err := http.NewRequestWithContext(ctx, "POST", u, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	hr.Header.Set("Authorization", "Bearer "+c.Token)
	hr.Header.Set("Content-Type", "application/json")
	hc := c.HTTP
	if hc == nil {
		hc = &http.Client{Timeout: 60 * time.Second}
	}
	resp, err := hc.Do(hr)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if err != nil {
		return nil, err
	}
	return ParseResponse(resp.StatusCode, raw)
}

// ParseResponse decodes the Cloudflare envelope {result, success, errors} (also accepts a
// bare result object, as returned by Workers bindings / fakes).
func ParseResponse(status int, raw []byte) (*Response, error) {
	var env struct {
		Result  *Response `json:"result"`
		Success *bool     `json:"success"`
		Errors  []struct {
			Code    int    `json:"code"`
			Message string `json:"message"`
		} `json:"errors"`
	}
	_ = json.Unmarshal(raw, &env)
	if status < 200 || status > 299 || (env.Success != nil && !*env.Success) {
		msg := strings.TrimSpace(string(raw))
		if len(env.Errors) > 0 {
			var parts []string
			for _, e := range env.Errors {
				parts = append(parts, fmt.Sprintf("%d %s", e.Code, e.Message))
			}
			msg = strings.Join(parts, "; ")
		}
		if len(msg) > 300 {
			msg = msg[:300]
		}
		if status >= 200 && status <= 299 {
			status = 502
		}
		return nil, &APIError{Status: status, Message: msg}
	}
	res := env.Result
	if res == nil {
		res = &Response{}
		if err := json.Unmarshal(raw, res); err != nil {
			return nil, fmt.Errorf("clef: decode response: %w", err)
		}
	}
	if res.Answers == nil {
		return nil, &APIError{Status: 502, Message: "response without answers"}
	}
	return res, nil
}

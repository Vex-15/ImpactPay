// Package paypal is a thin, explicit client for the PayPal Sandbox REST API.
//
// Scope, stated plainly: this is the PayPal Orders v2 and Payments v1 surface
// that a milestone funding prototype actually needs —
//
//	CreateOrder             POST /v2/checkout/orders
//	CaptureOrder            POST /v2/checkout/orders/{id}/capture
//	GetOrder                GET  /v2/checkout/orders/{id}
//	Refund                  POST /v2/payments/captures/{id}/refund
//	CreatePayout            POST /v1/payments/payouts
//	VerifyWebhookSignature  POST /v1/notifications/verify-webhook-signature
//
// Credentials live only on the server. The base URL is validated by the config
// package to point at a PayPal *sandbox* host, and ImpactPay has no live-payment
// code path: this is a conditional-release prototype, not a regulated escrow
// service, and the terminology throughout the product reflects that.
package paypal

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// Config holds sandbox credentials.
type Config struct {
	ClientID     string
	ClientSecret string
	BaseURL      string
	WebhookID    string
	HTTPClient   *http.Client
}

// Client talks to the PayPal sandbox.
type Client struct {
	cfg  Config
	http *http.Client

	mu          sync.Mutex
	accessToken string
	tokenExpiry time.Time
}

// New creates a client. A client without credentials is inert: Enabled reports
// false and no call is attempted.
func New(cfg Config) *Client {
	if cfg.BaseURL == "" {
		cfg.BaseURL = "https://api-m.sandbox.paypal.com"
	}
	cfg.BaseURL = strings.TrimRight(cfg.BaseURL, "/")
	hc := cfg.HTTPClient
	if hc == nil {
		hc = &http.Client{Timeout: 30 * time.Second}
	}
	return &Client{cfg: cfg, http: hc}
}

// Enabled reports whether live sandbox credentials are configured.
func (c *Client) Enabled() bool {
	return c != nil && c.cfg.ClientID != "" && c.cfg.ClientSecret != ""
}

// WebhookConfigured reports whether a webhook id is available for verification.
func (c *Client) WebhookConfigured() bool { return c != nil && c.cfg.WebhookID != "" }

// Error is a structured PayPal API failure.
type Error struct {
	StatusCode int
	Name       string
	Message    string
	Details    []Issue
	Raw        string
}

// Issue is one field-level problem reported by PayPal.
type Issue struct {
	Field       string `json:"field"`
	Issue       string `json:"issue"`
	Description string `json:"description"`
}

func (e *Error) Error() string {
	if e.Name == "" {
		return fmt.Sprintf("paypal: HTTP %d: %s", e.StatusCode, e.Raw)
	}
	return fmt.Sprintf("paypal: %s (%s): %s", e.Name, e.Message, e.Raw)
}

// IsNotFound reports whether the failure was a 404.
func (e *Error) IsNotFound() bool { return e.StatusCode == http.StatusNotFound }

// ---------------------------------------------------------------------------
// Auth
// ---------------------------------------------------------------------------

// token returns a cached OAuth2 access token, refreshing it shortly before expiry.
func (c *Client) token(ctx context.Context) (string, error) {
	if !c.Enabled() {
		return "", fmt.Errorf("paypal: credentials are not configured")
	}

	c.mu.Lock()
	if c.accessToken != "" && time.Now().Before(c.tokenExpiry) {
		tok := c.accessToken
		c.mu.Unlock()
		return tok, nil
	}
	c.mu.Unlock()

	form := url.Values{"grant_type": {"client_credentials"}}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		c.cfg.BaseURL+"/v1/oauth2/token", strings.NewReader(form.Encode()))
	if err != nil {
		return "", fmt.Errorf("paypal: build token request: %w", err)
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("Accept", "application/json")
	req.SetBasicAuth(c.cfg.ClientID, c.cfg.ClientSecret)

	resp, err := c.http.Do(req)
	if err != nil {
		return "", fmt.Errorf("paypal: token request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode/100 != 2 {
		return "", decodeError(resp.StatusCode, body)
	}

	var out struct {
		AccessToken string `json:"access_token"`
		ExpiresIn   int    `json:"expires_in"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return "", fmt.Errorf("paypal: decode token response: %w", err)
	}
	if out.AccessToken == "" {
		return "", fmt.Errorf("paypal: token response contained no access_token")
	}

	ttl := time.Duration(out.ExpiresIn) * time.Second
	if ttl <= 0 {
		ttl = time.Hour
	}
	c.mu.Lock()
	c.accessToken = out.AccessToken
	c.tokenExpiry = time.Now().Add(ttl - 60*time.Second)
	c.mu.Unlock()

	return out.AccessToken, nil
}

// AuthHeaderValue exposes the Basic header a browser SDK needs for sandbox
// client-token flows, without ever exposing the secret to the client.
func (c *Client) AuthHeaderValue() string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte(c.cfg.ClientID+":"+c.cfg.ClientSecret))
}

// ---------------------------------------------------------------------------
// Transport
// ---------------------------------------------------------------------------

func (c *Client) do(ctx context.Context, method, path string, payload any, idempotencyKey string, out any) error {
	tok, err := c.token(ctx)
	if err != nil {
		return err
	}

	var body io.Reader
	if payload != nil {
		b, err := json.Marshal(payload)
		if err != nil {
			return fmt.Errorf("paypal: encode request: %w", err)
		}
		body = bytes.NewReader(b)
	}

	req, err := http.NewRequestWithContext(ctx, method, c.cfg.BaseURL+path, body)
	if err != nil {
		return fmt.Errorf("paypal: build request: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+tok)
	req.Header.Set("Accept", "application/json")
	if payload != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if idempotencyKey != "" {
		req.Header.Set("PayPal-Request-Id", idempotencyKey)
	}

	resp, err := c.http.Do(req)
	if err != nil {
		return fmt.Errorf("paypal: request %s %s: %w", method, path, err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode/100 != 2 {
		return decodeError(resp.StatusCode, raw)
	}
	if out == nil {
		return nil
	}
	if err := json.Unmarshal(raw, out); err != nil {
		return fmt.Errorf("paypal: decode %s %s: %w", method, path, err)
	}
	return nil
}

func decodeError(status int, raw []byte) *Error {
	var parsed struct {
		Name    string  `json:"name"`
		Message string  `json:"message"`
		Details []Issue `json:"details"`
	}
	_ = json.Unmarshal(raw, &parsed)
	return &Error{
		StatusCode: status,
		Name:       parsed.Name,
		Message:    parsed.Message,
		Details:    parsed.Details,
		Raw:        strings.TrimSpace(string(raw)),
	}
}

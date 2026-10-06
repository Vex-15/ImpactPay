package storage

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"sort"
	"strings"
	"time"
)

// S3Config configures the S3-compatible driver. It works against AWS S3,
// Cloudflare R2 and MinIO by overriding the endpoint and forcing path style.
type S3Config struct {
	Bucket         string
	Region         string
	Endpoint       string // e.g. https://s3.eu-central-1.amazonaws.com
	AccessKey      string
	SecretKey      string
	SessionToken   string
	ForcePathStyle bool
	Prefix         string
	HTTPClient     *http.Client
}

// S3 stores objects in an S3-compatible bucket. Render has an ephemeral
// filesystem, so this driver is the intended production storage backend.
type S3 struct {
	cfg    S3Config
	client *http.Client
}

// NewS3 validates configuration and returns the driver.
func NewS3(cfg S3Config) (*S3, error) {
	if cfg.Bucket == "" || cfg.Region == "" || cfg.AccessKey == "" || cfg.SecretKey == "" {
		return nil, fmt.Errorf("storage: s3 requires bucket, region, access key and secret key")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = fmt.Sprintf("https://s3.%s.amazonaws.com", cfg.Region)
	}
	cfg.Endpoint = strings.TrimRight(cfg.Endpoint, "/")
	client := cfg.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 60 * time.Second}
	}
	return &S3{cfg: cfg, client: client}, nil
}

// Driver implements Store.
func (s *S3) Driver() string { return "s3" }

// URL implements Store.
func (s *S3) URL(key string) string {
	key = s.objectKey(key)
	if s.cfg.ForcePathStyle {
		return fmt.Sprintf("%s/%s/%s", s.cfg.Endpoint, s.cfg.Bucket, escapePath(key))
	}
	// Virtual-hosted style.
	u, err := url.Parse(s.cfg.Endpoint)
	if err != nil {
		return fmt.Sprintf("%s/%s/%s", s.cfg.Endpoint, s.cfg.Bucket, escapePath(key))
	}
	u.Host = s.cfg.Bucket + "." + u.Host
	u.Path = "/" + escapePath(key)
	return u.String()
}

func (s *S3) objectKey(key string) string {
	key = strings.TrimPrefix(key, "/")
	if s.cfg.Prefix != "" {
		return strings.Trim(s.cfg.Prefix, "/") + "/" + key
	}
	return key
}

// Put implements Store. Objects are buffered so the payload hash can be included
// in the signature; uploads are bounded by the API-layer size limit.
func (s *S3) Put(ctx context.Context, key string, r io.Reader, contentType string) (Object, error) {
	buf, err := io.ReadAll(r)
	if err != nil {
		return Object{}, fmt.Errorf("storage: s3 read payload: %w", err)
	}
	sum := sha256.Sum256(buf)

	objKey := s.objectKey(key)
	req, err := s.newRequest(ctx, http.MethodPut, objKey, nil, bytes.NewReader(buf))
	if err != nil {
		return Object{}, err
	}
	req.Header.Set("Content-Type", contentType)
	req.ContentLength = int64(len(buf))
	req.Header.Set("x-amz-content-sha256", hex.EncodeToString(sum[:]))
	s.sign(req, sum[:])

	resp, err := s.client.Do(req)
	if err != nil {
		return Object{}, fmt.Errorf("storage: s3 put: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		return Object{}, fmt.Errorf("storage: s3 put %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}

	return Object{
		Key:         key,
		Size:        int64(len(buf)),
		ContentHash: hex.EncodeToString(sum[:]),
		ContentType: contentType,
	}, nil
}

// Get implements Store.
func (s *S3) Get(ctx context.Context, key string) (io.ReadCloser, Object, error) {
	req, err := s.newRequest(ctx, http.MethodGet, s.objectKey(key), nil, nil)
	if err != nil {
		return nil, Object{}, err
	}
	req.Header.Set("x-amz-content-sha256", emptyPayloadHash)
	s.sign(req, []byte(emptyPayloadHash))

	resp, err := s.client.Do(req)
	if err != nil {
		return nil, Object{}, fmt.Errorf("storage: s3 get: %w", err)
	}
	if resp.StatusCode == http.StatusNotFound {
		_ = resp.Body.Close()
		return nil, Object{}, ErrNotFound
	}
	if resp.StatusCode/100 != 2 {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
		_ = resp.Body.Close()
		return nil, Object{}, fmt.Errorf("storage: s3 get %s: %s", resp.Status, strings.TrimSpace(string(body)))
	}
	return resp.Body, Object{
		Key:         key,
		Size:        resp.ContentLength,
		ContentType: resp.Header.Get("Content-Type"),
	}, nil
}

// Delete implements Store.
func (s *S3) Delete(ctx context.Context, key string) error {
	req, err := s.newRequest(ctx, http.MethodDelete, s.objectKey(key), nil, nil)
	if err != nil {
		return err
	}
	req.Header.Set("x-amz-content-sha256", emptyPayloadHash)
	s.sign(req, []byte(emptyPayloadHash))

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("storage: s3 delete: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode == http.StatusNotFound || resp.StatusCode/100 == 2 {
		return nil
	}
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	return fmt.Errorf("storage: s3 delete %s: %s", resp.Status, strings.TrimSpace(string(body)))
}

func (s *S3) newRequest(ctx context.Context, method, objKey string, query url.Values, body io.Reader) (*http.Request, error) {
	var rawURL string
	if s.cfg.ForcePathStyle {
		rawURL = fmt.Sprintf("%s/%s/%s", s.cfg.Endpoint, s.cfg.Bucket, escapePath(objKey))
	} else {
		u, err := url.Parse(s.cfg.Endpoint)
		if err != nil {
			return nil, fmt.Errorf("storage: s3 endpoint: %w", err)
		}
		u.Host = s.cfg.Bucket + "." + u.Host
		u.Path = "/" + escapePath(objKey)
		rawURL = u.String()
	}
	if len(query) > 0 {
		rawURL += "?" + query.Encode()
	}
	return http.NewRequestWithContext(ctx, method, rawURL, body)
}

const emptyPayloadHash = "e3b0c44298fc1c149afbf4c8996fb92427ae41e4649b934ca495991b7852b855"

// sign applies AWS Signature Version 4 to the request.
func (s *S3) sign(req *http.Request, payload []byte) {
	now := time.Now().UTC()
	amzDate := now.Format("20060102T150405Z")
	dateStamp := now.Format("20060102")

	req.Header.Set("x-amz-date", amzDate)
	req.Header.Set("Host", req.URL.Host)
	if s.cfg.SessionToken != "" {
		req.Header.Set("x-amz-security-token", s.cfg.SessionToken)
	}

	payloadHash := hex.EncodeToString(payload[:])
	if h := req.Header.Get("x-amz-content-sha256"); h != "" {
		payloadHash = h
	}
	req.Header.Set("x-amz-content-sha256", payloadHash)

	canonical := buildCanonicalRequest(req, payloadHash)
	scope := strings.Join([]string{dateStamp, s.cfg.Region, "s3", "aws4_request"}, "/")

	stringToSign := strings.Join([]string{
		"AWS4-HMAC-SHA256",
		amzDate,
		scope,
		hex.EncodeToString(sha256Sum([]byte(canonical))),
	}, "\n")

	key := deriveSigningKey(s.cfg.SecretKey, dateStamp, s.cfg.Region, "s3")
	signature := hex.EncodeToString(hmacSHA256(key, []byte(stringToSign)))

	signedHeaders := strings.Join(canonicalHeaderNames(req), ";")
	req.Header.Set("Authorization", fmt.Sprintf(
		"AWS4-HMAC-SHA256 Credential=%s/%s, SignedHeaders=%s, Signature=%s",
		s.cfg.AccessKey, scope, signedHeaders, signature))
}

// buildCanonicalRequest constructs the SigV4 canonical request string.
func buildCanonicalRequest(req *http.Request, payloadHash string) string {
	headers := canonicalHeaderNames(req)
	var hb strings.Builder
	for _, h := range headers {
		hb.WriteString(h)
		hb.WriteString(":")
		hb.WriteString(strings.Join(strings.Fields(req.Header.Get(h)), " "))
		hb.WriteString("\n")
	}
	return strings.Join([]string{
		req.Method,
		canonicalURI(req),
		canonicalQuery(req),
		hb.String(),
		strings.Join(headers, ";"),
		payloadHash,
	}, "\n")
}

func canonicalURI(req *http.Request) string {
	p := req.URL.EscapedPath()
	if p == "" {
		return "/"
	}
	return p
}

func canonicalQuery(req *http.Request) string {
	q := req.URL.Query()
	if len(q) == 0 {
		return ""
	}
	keys := make([]string, 0, len(q))
	for k := range q {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var parts []string
	for _, k := range keys {
		vals := q[k]
		sort.Strings(vals)
		for _, v := range vals {
			parts = append(parts, url.QueryEscape(k)+"="+url.QueryEscape(v))
		}
	}
	return strings.Join(parts, "&")
}

// canonicalHeaderNames returns the sorted, lowercased signed header list. Only
// headers we control are signed, which keeps the signature stable.
func canonicalHeaderNames(req *http.Request) []string {
	names := []string{"host"}
	for h := range req.Header {
		l := strings.ToLower(h)
		if l == "host" || l == "content-length" || l == "user-agent" || l == "authorization" {
			continue
		}
		names = append(names, l)
	}
	sort.Strings(names)
	out := names[:0]
	var prev string
	for _, n := range names {
		if n == prev {
			continue
		}
		out = append(out, n)
		prev = n
	}
	return out
}

func sha256Sum(b []byte) []byte {
	s := sha256.Sum256(b)
	return s[:]
}

func hmacSHA256(key, data []byte) []byte {
	m := hmac.New(sha256.New, key)
	m.Write(data)
	return m.Sum(nil)
}

func deriveSigningKey(secret, dateStamp, region, service string) []byte {
	kDate := hmacSHA256([]byte("AWS4"+secret), []byte(dateStamp))
	kRegion := hmacSHA256(kDate, []byte(region))
	kService := hmacSHA256(kRegion, []byte(service))
	return hmacSHA256(kService, []byte("aws4_request"))
}

// escapePath percent-encodes each path segment while preserving separators.
func escapePath(p string) string {
	segs := strings.Split(p, "/")
	for i, s := range segs {
		segs[i] = url.PathEscape(s)
	}
	return strings.Join(segs, "/")
}

// S3FromEnv builds the driver from the documented environment variables.
func S3FromEnv() (*S3, error) {
	return NewS3(S3Config{
		Bucket:         os.Getenv("S3_BUCKET"),
		Region:         envOr("S3_REGION", "us-east-1"),
		Endpoint:       os.Getenv("S3_ENDPOINT"),
		AccessKey:      os.Getenv("S3_ACCESS_KEY_ID"),
		SecretKey:      os.Getenv("S3_SECRET_ACCESS_KEY"),
		SessionToken:   os.Getenv("S3_SESSION_TOKEN"),
		ForcePathStyle: envOr("S3_FORCE_PATH_STYLE", "false") == "true",
		Prefix:         os.Getenv("S3_PREFIX"),
	})
}

func envOr(k, def string) string {
	if v := strings.TrimSpace(os.Getenv(k)); v != "" {
		return v
	}
	return def
}

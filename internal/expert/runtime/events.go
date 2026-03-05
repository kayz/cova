package runtime

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"strings"
	"time"

	"cova/pkg/apiv1"
)

type TaskCompletedEvent struct {
	SpecVersion     string            `json:"specversion"`
	ID              string            `json:"id"`
	Type            string            `json:"type"`
	Source          string            `json:"source"`
	Time            string            `json:"time"`
	DataContentType string            `json:"datacontenttype"`
	Data            TaskCompletedData `json:"data"`
}

type TaskCompletedData struct {
	TaskID  string             `json:"task_id"`
	JobID   string             `json:"job_id"`
	TraceID string             `json:"trace_id,omitempty"`
	Status  string             `json:"status"`
	Result  apiv1.ExpertResult `json:"result"`
}

type HTTPEventSink struct {
	client  *http.Client
	url     string
	secret  string
	timeout time.Duration
}

func NewHTTPEventSink(url string, secret string, timeout time.Duration) *HTTPEventSink {
	if timeout <= 0 {
		timeout = 5 * time.Second
	}
	return &HTTPEventSink{
		client:  http.DefaultClient,
		url:     strings.TrimSpace(url),
		secret:  strings.TrimSpace(secret),
		timeout: timeout,
	}
}

func (s *HTTPEventSink) SendTaskCompleted(ctx context.Context, event TaskCompletedEvent) error {
	if strings.TrimSpace(s.url) == "" {
		return nil
	}
	raw, err := json.Marshal(event)
	if err != nil {
		return fmt.Errorf("marshal event: %w", err)
	}

	reqCtx, cancel := context.WithTimeout(ctx, s.timeout)
	defer cancel()

	req, err := http.NewRequestWithContext(reqCtx, http.MethodPost, s.url, bytes.NewReader(raw))
	if err != nil {
		return fmt.Errorf("build request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	if s.secret != "" {
		ts := strconv.FormatInt(time.Now().Unix(), 10)
		req.Header.Set("X-COVA-Timestamp", ts)
		req.Header.Set("X-COVA-Signature", "sha256="+signPayload(s.secret, ts, raw))
	}

	resp, err := s.client.Do(req)
	if err != nil {
		return fmt.Errorf("post event: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("event endpoint status=%d", resp.StatusCode)
	}
	return nil
}

func signPayload(secret string, timestamp string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write([]byte(timestamp))
	mac.Write([]byte("."))
	mac.Write(body)
	return hex.EncodeToString(mac.Sum(nil))
}

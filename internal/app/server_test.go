package app

import (
	"bytes"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
)

type fakeQueue struct {
	reasons []string
	status  SyncStatus
}

func (q *fakeQueue) Enqueue(reason string) bool {
	q.reasons = append(q.reasons, reason)
	return true
}

func (q *fakeQueue) Status() SyncStatus {
	return q.status
}

func testConfig() Config {
	return Config{
		WebhookSecret:    []byte("secret"),
		GitHubRepository: "Lowess/docker-templates-unraid",
		GitHubBranch:     "main",
		MaxBodyBytes:     1_048_576,
	}
}

func TestGitHubDocumentedSignatureVector(t *testing.T) {
	secret := []byte("It's a Secret to Everybody")
	body := []byte("Hello, World!")
	signature := "sha256=757107ea0eb2509fc211221cce984b8a37570b6d7586c22c46f4379c8b043e17"
	if !validSignature(secret, body, signature) {
		t.Fatal("expected documented signature to validate")
	}
	if validSignature(secret, append(body, '!'), signature) {
		t.Fatal("expected modified payload to fail validation")
	}
}

func TestValidPushIsQueued(t *testing.T) {
	queue := &fakeQueue{status: SyncStatus{State: "ok"}}
	server := NewWebhookServer(testConfig(), queue, slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload := []byte(`{"repository":{"full_name":"Lowess/docker-templates-unraid"},"ref":"refs/heads/main","deleted":false}`)
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	request.Header.Set("X-GitHub-Event", "push")
	request.Header.Set("X-GitHub-Delivery", "delivery-1")
	request.Header.Set("X-Hub-Signature-256", sign([]byte("secret"), payload))
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusAccepted {
		t.Fatalf("expected 202, got %d: %s", response.Code, response.Body.String())
	}
	if len(queue.reasons) != 1 || queue.reasons[0] != "GitHub delivery delivery-1" {
		t.Fatalf("unexpected queued reasons: %#v", queue.reasons)
	}
}

func TestInvalidSignatureIsRejected(t *testing.T) {
	queue := &fakeQueue{}
	server := NewWebhookServer(testConfig(), queue, slog.New(slog.NewTextHandler(io.Discard, nil)))
	payload, _ := json.Marshal(map[string]any{
		"repository": map[string]string{"full_name": "Lowess/docker-templates-unraid"},
		"ref":        "refs/heads/main",
	})
	request := httptest.NewRequest(http.MethodPost, "/webhook", bytes.NewReader(payload))
	request.Header.Set("X-GitHub-Event", "push")
	request.Header.Set("X-GitHub-Delivery", "delivery-2")
	request.Header.Set("X-Hub-Signature-256", "sha256="+hex.EncodeToString(make([]byte, sha256.Size)))
	response := httptest.NewRecorder()

	server.Handler().ServeHTTP(response, request)
	if response.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d", response.Code)
	}
	if len(queue.reasons) != 0 {
		t.Fatal("invalid request was queued")
	}
}

func sign(secret, payload []byte) string {
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(payload)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

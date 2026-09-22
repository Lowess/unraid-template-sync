package app

import (
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os/signal"
	"strings"
	"sync"
	"syscall"
	"time"
)

type WebhookServer struct {
	config     Config
	queue      SyncQueue
	logger     *slog.Logger
	deliveries map[string]time.Time
	mu         sync.Mutex
}

func NewWebhookServer(config Config, queue SyncQueue, logger *slog.Logger) *WebhookServer {
	return &WebhookServer{
		config:     config,
		queue:      queue,
		logger:     logger,
		deliveries: make(map[string]time.Time),
	}
}

func (s *WebhookServer) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/healthz", s.handleHealth)
	mux.HandleFunc("/webhook", s.handleWebhook)
	return securityHeaders(mux)
}

func (s *WebhookServer) handleHealth(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodGet {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	status := s.queue.Status()
	healthy := status.State != "error"
	responseStatus := http.StatusOK
	if !healthy {
		responseStatus = http.StatusServiceUnavailable
	}
	writeJSON(writer, responseStatus, map[string]any{
		"healthy": healthy,
		"sync":    status,
	})
}

func (s *WebhookServer) handleWebhook(writer http.ResponseWriter, request *http.Request) {
	if request.Method != http.MethodPost {
		writeJSON(writer, http.StatusMethodNotAllowed, map[string]string{"error": "method not allowed"})
		return
	}
	if request.ContentLength > s.config.MaxBodyBytes {
		writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": "payload too large"})
		return
	}
	request.Body = http.MaxBytesReader(writer, request.Body, s.config.MaxBodyBytes)
	body, err := io.ReadAll(request.Body)
	if err != nil {
		var maxBytesError *http.MaxBytesError
		if errors.As(err, &maxBytesError) {
			writeJSON(writer, http.StatusRequestEntityTooLarge, map[string]string{"error": "payload too large"})
			return
		}
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "could not read payload"})
		return
	}
	if !validSignature(s.config.WebhookSecret, body, request.Header.Get("X-Hub-Signature-256")) {
		writeJSON(writer, http.StatusUnauthorized, map[string]string{"error": "invalid signature"})
		return
	}

	deliveryID := strings.TrimSpace(request.Header.Get("X-GitHub-Delivery"))
	event := strings.TrimSpace(request.Header.Get("X-GitHub-Event"))
	if deliveryID == "" || event == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "missing GitHub headers"})
		return
	}
	var payload struct {
		Repository struct {
			FullName string `json:"full_name"`
		} `json:"repository"`
		Ref     string `json:"ref"`
		Deleted bool   `json:"deleted"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || payload.Repository.FullName == "" {
		writeJSON(writer, http.StatusBadRequest, map[string]string{"error": "invalid GitHub payload"})
		return
	}
	if !strings.EqualFold(payload.Repository.FullName, s.config.GitHubRepository) {
		writeJSON(writer, http.StatusForbidden, map[string]string{"error": "repository is not allowed"})
		return
	}
	if !s.rememberDelivery(deliveryID) {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "duplicate ignored"})
		return
	}
	if event == "ping" {
		writeJSON(writer, http.StatusOK, map[string]string{"status": "pong"})
		return
	}
	if event != "push" {
		writeJSON(writer, http.StatusAccepted, map[string]string{"status": "event ignored"})
		return
	}
	if payload.Ref != "refs/heads/"+s.config.GitHubBranch || payload.Deleted {
		writeJSON(writer, http.StatusAccepted, map[string]string{"status": "ref ignored"})
		return
	}

	queued := s.queue.Enqueue("GitHub delivery " + deliveryID)
	status := "sync queued"
	if !queued {
		status = "sync already queued"
	}
	s.logger.Info("accepted GitHub webhook", "delivery", deliveryID, "queued", queued)
	writeJSON(writer, http.StatusAccepted, map[string]string{"status": status})
}

func (s *WebhookServer) rememberDelivery(deliveryID string) bool {
	now := time.Now()
	cutoff := now.Add(-24 * time.Hour)
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, receivedAt := range s.deliveries {
		if receivedAt.Before(cutoff) {
			delete(s.deliveries, id)
		}
	}
	if _, exists := s.deliveries[deliveryID]; exists {
		return false
	}
	if len(s.deliveries) >= 1_000 {
		var oldestID string
		var oldestTime time.Time
		for id, receivedAt := range s.deliveries {
			if oldestID == "" || receivedAt.Before(oldestTime) {
				oldestID = id
				oldestTime = receivedAt
			}
		}
		delete(s.deliveries, oldestID)
	}
	s.deliveries[deliveryID] = now
	return true
}

func validSignature(secret, body []byte, supplied string) bool {
	const prefix = "sha256="
	if !strings.HasPrefix(supplied, prefix) {
		return false
	}
	decoded, err := hex.DecodeString(strings.TrimPrefix(supplied, prefix))
	if err != nil {
		return false
	}
	mac := hmac.New(sha256.New, secret)
	_, _ = mac.Write(body)
	return hmac.Equal(mac.Sum(nil), decoded)
}

func securityHeaders(next http.Handler) http.Handler {
	return http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		writer.Header().Set("Cache-Control", "no-store")
		writer.Header().Set("X-Content-Type-Options", "nosniff")
		next.ServeHTTP(writer, request)
	})
}

func writeJSON(writer http.ResponseWriter, status int, payload any) {
	writer.Header().Set("Content-Type", "application/json")
	writer.WriteHeader(status)
	_ = json.NewEncoder(writer).Encode(payload)
}

func Run(logger *slog.Logger) error {
	config, err := LoadConfig()
	if err != nil {
		return err
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	worker := NewWorker(NewGitSynchronizer(config), logger)
	worker.Start(ctx)
	if config.SyncOnStart {
		worker.Enqueue("container startup")
	}

	address := fmt.Sprintf("%s:%d", config.ListenAddress, config.Port)
	server := &http.Server{
		Addr:              address,
		Handler:           NewWebhookServer(config, worker, logger).Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
		MaxHeaderBytes:    32 << 10,
	}
	serverErrors := make(chan error, 1)
	go func() {
		logger.Info("webhook service listening", "address", address)
		serverErrors <- server.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownContext, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		return server.Shutdown(shutdownContext)
	case err := <-serverErrors:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

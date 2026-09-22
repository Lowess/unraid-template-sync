package app

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"
)

type Synchronizer interface {
	Sync(context.Context) (string, error)
}

type GitSynchronizer struct {
	config Config
}

func NewGitSynchronizer(config Config) *GitSynchronizer {
	return &GitSynchronizer{config: config}
}

func (s *GitSynchronizer) Sync(parent context.Context) (string, error) {
	ctx, cancel := context.WithTimeout(parent, s.config.SyncTimeout)
	defer cancel()

	gitDirectory := filepath.Join(s.config.RepoDir, ".git")
	if info, err := os.Stat(gitDirectory); err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a Git working tree", s.config.RepoDir)
	}
	if info, err := os.Stat(s.config.SourceDir); err != nil || !info.IsDir() {
		return "", fmt.Errorf("template source directory does not exist: %s", s.config.SourceDir)
	}

	remoteRef := fmt.Sprintf("refs/remotes/%s/%s", s.config.GitRemoteTracking, s.config.GitHubBranch)
	refspec := fmt.Sprintf("+refs/heads/%s:%s", s.config.GitHubBranch, remoteRef)
	if _, err := s.git(ctx, "fetch", "--prune", "--", s.config.GitRemoteURL, refspec); err != nil {
		return "", err
	}
	if _, err := s.git(ctx, "reset", "--hard", remoteRef); err != nil {
		return "", err
	}
	if s.config.GitClean {
		if _, err := s.git(ctx, "clean", "-fd"); err != nil {
			return "", err
		}
	}
	if err := mirrorXML(s.config.SourceDir, s.config.DestinationDir); err != nil {
		return "", err
	}
	return s.git(ctx, "rev-parse", "--short", "HEAD")
}

func (s *GitSynchronizer) git(ctx context.Context, arguments ...string) (string, error) {
	base := []string{
		"-c", "safe.directory=" + s.config.RepoDir,
		"-C", s.config.RepoDir,
	}
	command := exec.CommandContext(ctx, "git", append(base, arguments...)...)
	command.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	output, err := command.CombinedOutput()
	if err != nil {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return "", fmt.Errorf("git %s timed out", arguments[0])
		}
		return "", fmt.Errorf("git %s failed: %w: %s", arguments[0], err, strings.TrimSpace(string(output)))
	}
	return strings.TrimSpace(string(output)), nil
}

func mirrorXML(sourceDir, destinationDir string) error {
	if err := os.MkdirAll(destinationDir, 0o755); err != nil {
		return fmt.Errorf("create template destination: %w", err)
	}
	sourceEntries, err := os.ReadDir(sourceDir)
	if err != nil {
		return fmt.Errorf("read template source: %w", err)
	}

	sourceFiles := make(map[string]struct{})
	for _, entry := range sourceEntries {
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".xml") || entry.Type()&os.ModeSymlink != 0 {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect source template %s: %w", entry.Name(), err)
		}
		if !info.Mode().IsRegular() {
			continue
		}
		sourceFiles[entry.Name()] = struct{}{}
		if err := copyFileAtomic(
			filepath.Join(sourceDir, entry.Name()),
			filepath.Join(destinationDir, entry.Name()),
		); err != nil {
			return err
		}
	}

	destinationEntries, err := os.ReadDir(destinationDir)
	if err != nil {
		return fmt.Errorf("read template destination: %w", err)
	}
	for _, entry := range destinationEntries {
		if !strings.EqualFold(filepath.Ext(entry.Name()), ".xml") {
			continue
		}
		if _, exists := sourceFiles[entry.Name()]; exists {
			continue
		}
		info, err := entry.Info()
		if err != nil {
			return fmt.Errorf("inspect destination template %s: %w", entry.Name(), err)
		}
		if info.Mode().IsRegular() || entry.Type()&os.ModeSymlink != 0 {
			if err := os.Remove(filepath.Join(destinationDir, entry.Name())); err != nil {
				return fmt.Errorf("remove stale template %s: %w", entry.Name(), err)
			}
		}
	}
	return nil
}

func copyFileAtomic(source, destination string) error {
	sourceHandle, err := os.Open(source)
	if err != nil {
		return fmt.Errorf("open source template %s: %w", source, err)
	}
	defer sourceHandle.Close()

	temporary, err := os.CreateTemp(filepath.Dir(destination), ".template-*")
	if err != nil {
		return fmt.Errorf("create temporary template: %w", err)
	}
	temporaryName := temporary.Name()
	defer os.Remove(temporaryName)

	if _, err := io.Copy(temporary, sourceHandle); err != nil {
		temporary.Close()
		return fmt.Errorf("copy template %s: %w", source, err)
	}
	if err := temporary.Chmod(0o644); err != nil {
		temporary.Close()
		return fmt.Errorf("set template permissions: %w", err)
	}
	if err := temporary.Sync(); err != nil {
		temporary.Close()
		return fmt.Errorf("flush template %s: %w", source, err)
	}
	if err := temporary.Close(); err != nil {
		return fmt.Errorf("close template %s: %w", source, err)
	}
	if err := os.Rename(temporaryName, destination); err != nil {
		return fmt.Errorf("publish template %s: %w", destination, err)
	}
	return nil
}

type SyncStatus struct {
	State           string     `json:"state"`
	LastReason      string     `json:"last_reason,omitempty"`
	LastCommit      string     `json:"last_commit,omitempty"`
	LastCompletedAt *time.Time `json:"last_completed_at,omitempty"`
	LastError       string     `json:"last_error,omitempty"`
}

type SyncQueue interface {
	Enqueue(string) bool
	Status() SyncStatus
}

type Worker struct {
	synchronizer Synchronizer
	logger       *slog.Logger
	requests     chan string
	mu           sync.RWMutex
	status       SyncStatus
}

func NewWorker(synchronizer Synchronizer, logger *slog.Logger) *Worker {
	return &Worker{
		synchronizer: synchronizer,
		logger:       logger,
		requests:     make(chan string, 1),
		status:       SyncStatus{State: "waiting"},
	}
}

func (w *Worker) Start(ctx context.Context) {
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case reason := <-w.requests:
				w.run(ctx, reason)
			}
		}
	}()
}

func (w *Worker) Enqueue(reason string) bool {
	select {
	case w.requests <- reason:
		return true
	default:
		return false
	}
}

func (w *Worker) Status() SyncStatus {
	w.mu.RLock()
	defer w.mu.RUnlock()
	return w.status
}

func (w *Worker) run(ctx context.Context, reason string) {
	w.mu.Lock()
	w.status.State = "syncing"
	w.status.LastReason = reason
	w.status.LastError = ""
	w.mu.Unlock()
	w.logger.Info("starting template sync", "reason", reason)

	commit, err := w.synchronizer.Sync(ctx)
	completedAt := time.Now().UTC()
	w.mu.Lock()
	w.status.LastCompletedAt = &completedAt
	if err != nil {
		w.status.State = "error"
		w.status.LastError = err.Error()
	} else {
		w.status.State = "ok"
		w.status.LastCommit = commit
		w.status.LastError = ""
	}
	w.mu.Unlock()

	if err != nil {
		w.logger.Error("template sync failed", "error", err)
		return
	}
	w.logger.Info("template sync completed", "commit", commit)
}

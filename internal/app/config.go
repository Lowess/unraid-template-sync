package app

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
	"unicode"
)

type Config struct {
	WebhookSecret     []byte
	GitHubRepository  string
	GitHubBranch      string
	GitRemoteURL      string
	GitRemoteTracking string
	RepoDir           string
	SourceDir         string
	DestinationDir    string
	ListenAddress     string
	Port              int
	MaxBodyBytes      int64
	SyncTimeout       time.Duration
	SyncOnStart       bool
	GitClean          bool
}

func LoadConfig() (Config, error) {
	secret := os.Getenv("WEBHOOK_SECRET")
	if secret == "" {
		return Config{}, errors.New("WEBHOOK_SECRET is required")
	}

	repository := envString("GITHUB_REPOSITORY", "Lowess/docker-templates-unraid")
	parts := strings.Split(repository, "/")
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return Config{}, errors.New("GITHUB_REPOSITORY must use the owner/repository form")
	}

	branch := envString("GITHUB_BRANCH", "main")
	if err := validateRefComponent(branch); err != nil {
		return Config{}, fmt.Errorf("GITHUB_BRANCH: %w", err)
	}
	tracking := envString("GIT_REMOTE_TRACKING_NAME", "origin")
	if err := validateRefComponent(tracking); err != nil {
		return Config{}, fmt.Errorf("GIT_REMOTE_TRACKING_NAME: %w", err)
	}

	repoDir, err := filepath.Abs(envString("REPO_DIR", "/repo"))
	if err != nil {
		return Config{}, fmt.Errorf("resolve REPO_DIR: %w", err)
	}
	sourceSubdir := envString("SOURCE_SUBDIR", "Lowess")
	if sourceSubdir == "" || filepath.IsAbs(sourceSubdir) {
		return Config{}, errors.New("SOURCE_SUBDIR must be relative")
	}
	sourceDir := filepath.Clean(filepath.Join(repoDir, sourceSubdir))
	relativeSource, err := filepath.Rel(repoDir, sourceDir)
	if err != nil || relativeSource == ".." || strings.HasPrefix(relativeSource, ".."+string(filepath.Separator)) {
		return Config{}, errors.New("SOURCE_SUBDIR must resolve inside REPO_DIR")
	}

	destinationDir, err := filepath.Abs(envString("DEST_DIR", "/templates"))
	if err != nil {
		return Config{}, fmt.Errorf("resolve DEST_DIR: %w", err)
	}
	relativeDestination, err := filepath.Rel(repoDir, destinationDir)
	if err != nil {
		return Config{}, fmt.Errorf("compare REPO_DIR and DEST_DIR: %w", err)
	}
	if relativeDestination == "." || (relativeDestination != ".." && !strings.HasPrefix(relativeDestination, ".."+string(filepath.Separator))) {
		return Config{}, errors.New("DEST_DIR must be outside REPO_DIR")
	}
	port, err := envInt("PORT", 9000, 1)
	if err != nil {
		return Config{}, err
	}
	if port > 65_535 {
		return Config{}, errors.New("PORT must not exceed 65535")
	}
	maxBodyBytes, err := envInt64("MAX_BODY_BYTES", 1_048_576, 1)
	if err != nil {
		return Config{}, err
	}
	syncTimeoutSeconds, err := envInt("SYNC_TIMEOUT_SECONDS", 120, 1)
	if err != nil {
		return Config{}, err
	}
	syncOnStart, err := envBool("SYNC_ON_START", true)
	if err != nil {
		return Config{}, err
	}
	gitClean, err := envBool("GIT_CLEAN", true)
	if err != nil {
		return Config{}, err
	}

	remoteURL := envString("GIT_REMOTE_URL", "https://github.com/Lowess/docker-templates-unraid.git")
	if remoteURL == "" {
		return Config{}, errors.New("GIT_REMOTE_URL is required")
	}

	return Config{
		WebhookSecret:     []byte(secret),
		GitHubRepository:  repository,
		GitHubBranch:      branch,
		GitRemoteURL:      remoteURL,
		GitRemoteTracking: tracking,
		RepoDir:           repoDir,
		SourceDir:         sourceDir,
		DestinationDir:    destinationDir,
		ListenAddress:     envString("LISTEN_ADDRESS", "0.0.0.0"),
		Port:              port,
		MaxBodyBytes:      maxBodyBytes,
		SyncTimeout:       time.Duration(syncTimeoutSeconds) * time.Second,
		SyncOnStart:       syncOnStart,
		GitClean:          gitClean,
	}, nil
}

func validateRefComponent(value string) error {
	if value == "" || strings.HasPrefix(value, "-") || strings.HasPrefix(value, ".") || strings.HasPrefix(value, "/") ||
		strings.HasSuffix(value, ".") || strings.HasSuffix(value, "/") || strings.Contains(value, "//") ||
		strings.Contains(value, "..") || strings.Contains(value, "@{") || strings.ContainsAny(value, `\~^:?*[`) ||
		strings.ContainsFunc(value, unicode.IsSpace) {
		return errors.New("not a safe Git ref component")
	}
	for _, part := range strings.Split(value, "/") {
		if strings.HasSuffix(part, ".lock") {
			return errors.New("not a safe Git ref component")
		}
	}
	return nil
}

func envString(name, fallback string) string {
	value, exists := os.LookupEnv(name)
	if !exists {
		return fallback
	}
	return strings.TrimSpace(value)
}

func envBool(name string, fallback bool) (bool, error) {
	value, exists := os.LookupEnv(name)
	if !exists {
		return fallback, nil
	}
	parsed, err := strconv.ParseBool(strings.TrimSpace(value))
	if err != nil {
		return false, fmt.Errorf("%s must be true or false", name)
	}
	return parsed, nil
}

func envInt(name string, fallback, minimum int) (int, error) {
	value, exists := os.LookupEnv(name)
	if !exists {
		return fallback, nil
	}
	parsed, err := strconv.Atoi(strings.TrimSpace(value))
	if err != nil || parsed < minimum {
		return 0, fmt.Errorf("%s must be an integer of at least %d", name, minimum)
	}
	return parsed, nil
}

func envInt64(name string, fallback, minimum int64) (int64, error) {
	value, exists := os.LookupEnv(name)
	if !exists {
		return fallback, nil
	}
	parsed, err := strconv.ParseInt(strings.TrimSpace(value), 10, 64)
	if err != nil || parsed < minimum {
		return 0, fmt.Errorf("%s must be an integer of at least %d", name, minimum)
	}
	return parsed, nil
}

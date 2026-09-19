package updater

import (
	"archive/tar"
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

const maxReleaseBytes = 512 << 20

var errUpdateBusy = errors.New("update deferred while jobs are active")

type Settings struct {
	Enabled            *bool  `yaml:"enabled" json:"enabled"`
	Channel            string `yaml:"channel" json:"channel"`
	Repository         string `yaml:"repository" json:"repository"`
	CheckIntervalHours int    `yaml:"check_interval_hours" json:"check_interval_hours"`
}

func (s *Settings) ApplyDefaults() {
	if s.Channel == "" {
		s.Channel = "stable"
	}
	if s.Repository == "" {
		s.Repository = "IamAngusU/ContextBridge"
	}
	if s.CheckIntervalHours <= 0 {
		s.CheckIntervalHours = 24
	}
}

func (s Settings) Validate() error {
	copy := s
	copy.ApplyDefaults()
	if copy.Channel != "stable" && copy.Channel != "preview" {
		return errors.New("updates.channel must be stable or preview")
	}
	parts := strings.Split(copy.Repository, "/")
	if len(parts) != 2 || !safeRepositoryPart(parts[0]) || !safeRepositoryPart(parts[1]) {
		return errors.New("updates.repository must use a safe owner/name")
	}
	if copy.CheckIntervalHours < 1 || copy.CheckIntervalHours > 24*30 {
		return errors.New("updates.check_interval_hours must be between 1 and 720")
	}
	return nil
}

func (s Settings) DefaultEnabled() bool {
	return s.Enabled != nil && *s.Enabled
}

type State struct {
	EnabledOverride *bool     `json:"enabled_override,omitempty"`
	LastChecked     time.Time `json:"last_checked,omitempty"`
	LastAvailable   string    `json:"last_available,omitempty"`
	LastInstalled   string    `json:"last_installed,omitempty"`
	PendingVersion  string    `json:"pending_version,omitempty"`
	LastError       string    `json:"last_error,omitempty"`
	BlockedVersion  string    `json:"blocked_version,omitempty"`
	RetryAfter      time.Time `json:"retry_after,omitempty"`
	Failures        int       `json:"failures,omitempty"`
	UpdatedAt       time.Time `json:"updated_at,omitempty"`
}

type Status struct {
	Enabled          bool      `json:"enabled"`
	Channel          string    `json:"channel"`
	Repository       string    `json:"repository"`
	CurrentVersion   string    `json:"current_version"`
	AvailableVersion string    `json:"available_version,omitempty"`
	UpdateAvailable  bool      `json:"update_available"`
	LastChecked      time.Time `json:"last_checked,omitempty"`
	LastInstalled    string    `json:"last_installed,omitempty"`
	PendingVersion   string    `json:"pending_version,omitempty"`
	LastError        string    `json:"last_error,omitempty"`
	BlockedVersion   string    `json:"blocked_version,omitempty"`
	RetryAfter       time.Time `json:"retry_after,omitempty"`
	ManagedBuild     bool      `json:"managed_build"`
}

type Result struct {
	Status          Status `json:"status"`
	Applied         bool   `json:"applied"`
	RestartRequired bool   `json:"restart_required"`
	TargetVersion   string `json:"target_version,omitempty"`
}

type Release struct {
	TagName    string  `json:"tag_name"`
	Draft      bool    `json:"draft"`
	Prerelease bool    `json:"prerelease"`
	Assets     []Asset `json:"assets"`
}

type Asset struct {
	Name               string `json:"name"`
	BrowserDownloadURL string `json:"browser_download_url"`
	Digest             string `json:"digest"`
	Size               int64  `json:"size"`
}

type Manager struct {
	settings       Settings
	dataDir        string
	currentVersion string
	executable     string
	client         *http.Client
	mu             sync.Mutex
	idleCheck      func(context.Context) bool
	healthURL      string
	configPath     string
}

func (m *Manager) SetConfigPath(path string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.configPath = path
}

func (m *Manager) SetHealthURL(value string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.healthURL = value
}

// SetIdleCheck supplies a fail-closed runtime check for automatic activation.
// Explicit `update apply` remains an operator action and does not use it.
func (m *Manager) SetIdleCheck(check func(context.Context) bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.idleCheck = check
}

func (m *Manager) idle(ctx context.Context) bool {
	return m.idleCheck != nil && m.idleCheck(ctx)
}

func New(settings Settings, dataDir, currentVersion string) (*Manager, error) {
	settings.ApplyDefaults()
	if err := settings.Validate(); err != nil {
		return nil, err
	}
	executable, err := os.Executable()
	if err != nil {
		return nil, err
	}
	executable, err = filepath.Abs(executable)
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(dataDir, 0700); err != nil {
		return nil, err
	}
	return &Manager{
		settings: settings, dataDir: dataDir, currentVersion: strings.TrimSpace(currentVersion), executable: executable,
		client: &http.Client{Timeout: 90 * time.Second, CheckRedirect: func(request *http.Request, via []*http.Request) error {
			if request.URL.Scheme != "https" || len(via) > 10 {
				return errors.New("update download redirected to an insecure or excessive destination")
			}
			return nil
		}},
	}, nil
}

func (m *Manager) LocalStatus() Status {
	// Status must remain responsive while a background update waits for a
	// cross-process lock or a slow release download. State writes use rename.
	state, _ := m.loadState()
	return m.status(state)
}

func (m *Manager) SetEnabled(enabled bool) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	unlock, err := m.acquireLock(ctx)
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	state, err := m.loadState()
	if err != nil {
		return Status{}, err
	}
	state.EnabledOverride = &enabled
	state.UpdatedAt = time.Now().UTC()
	state.LastError = ""
	if err := m.saveState(state); err != nil {
		return Status{}, err
	}
	return m.status(state), nil
}

func (m *Manager) Check(ctx context.Context) (Status, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.acquireLock(ctx)
	if err != nil {
		return Status{}, err
	}
	defer unlock()
	state, err := m.loadState()
	if err != nil {
		return Status{}, err
	}
	release, err := m.latest(ctx)
	state.LastChecked = time.Now().UTC()
	state.UpdatedAt = state.LastChecked
	if err != nil {
		state.LastError = cleanError(err)
		state.LastChecked = time.Time{}
		scheduleRetry(&state)
		_ = m.saveState(state)
		return m.status(state), err
	}
	state.LastAvailable = release.TagName
	state.LastError = ""
	state.RetryAfter = time.Time{}
	state.Failures = 0
	if err := m.saveState(state); err != nil {
		return Status{}, err
	}
	return m.status(state), nil
}

func (m *Manager) Auto(ctx context.Context) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.acquireLock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	state, err := m.loadState()
	if err != nil {
		return Result{}, err
	}
	status := m.status(state)
	if !status.Enabled || !status.ManagedBuild {
		return Result{Status: status}, nil
	}
	if time.Now().Before(state.RetryAfter) {
		return Result{Status: status}, nil
	}
	interval := time.Duration(m.settings.CheckIntervalHours) * time.Hour
	if state.LastChecked.IsZero() || time.Since(state.LastChecked) >= interval {
		release, checkErr := m.latest(ctx)
		state.LastChecked = time.Now().UTC()
		state.UpdatedAt = state.LastChecked
		if checkErr != nil {
			state.LastError = cleanError(checkErr)
			state.LastChecked = time.Time{}
			scheduleRetry(&state)
			_ = m.saveState(state)
			return Result{Status: m.status(state)}, checkErr
		}
		state.LastAvailable = release.TagName
		state.RetryAfter = time.Time{}
		state.Failures = 0
		if state.BlockedVersion != release.TagName {
			state.LastError = ""
		}
		if err := m.saveState(state); err != nil {
			return Result{}, err
		}
	}
	status = m.status(state)
	if !status.UpdateAvailable || state.BlockedVersion == state.LastAvailable || !m.idle(ctx) {
		return Result{Status: status}, nil
	}
	if !automaticInstallReady(ctx, m.executable) {
		// A Windows run/serve process cannot replace itself. A short-lived
		// installer-created Update task may do that when the managed task is
		// actually running; a hand-opened terminal is left untouched.
		return Result{Status: status}, nil
	}
	return m.applyLocked(ctx, state, false, true)
}

func (m *Manager) Apply(ctx context.Context, force bool) (Result, error) {
	m.mu.Lock()
	defer m.mu.Unlock()
	unlock, err := m.acquireLock(ctx)
	if err != nil {
		return Result{}, err
	}
	defer unlock()
	state, err := m.loadState()
	if err != nil {
		return Result{}, err
	}
	return m.applyLocked(ctx, state, force, false)
}

func (m *Manager) applyLocked(ctx context.Context, state State, force, automatic bool) (Result, error) {
	if !managedVersion(m.currentVersion) && !force {
		return Result{Status: m.status(state)}, errors.New("development builds are not replaced automatically; use --force to install the latest release")
	}
	release, err := m.latest(ctx)
	state.LastChecked = time.Now().UTC()
	state.UpdatedAt = state.LastChecked
	if err != nil {
		state.LastError = cleanError(err)
		state.LastChecked = time.Time{}
		if automatic {
			scheduleRetry(&state)
		}
		_ = m.saveState(state)
		return Result{Status: m.status(state)}, err
	}
	state.LastAvailable = release.TagName
	if !force && !newerVersion(m.currentVersion, release.TagName) {
		state.LastError = ""
		if err := m.saveState(state); err != nil {
			return Result{}, err
		}
		return Result{Status: m.status(state)}, nil
	}
	if automatic && !m.idle(ctx) {
		_ = m.saveState(state)
		return Result{Status: m.status(state)}, nil
	}
	restart, err := m.install(ctx, release, automatic)
	if err != nil {
		if automatic && errors.Is(err, errUpdateBusy) {
			_ = m.saveState(state)
			return Result{Status: m.status(state)}, nil
		}
		state.LastError = cleanError(err)
		if automatic {
			scheduleRetry(&state)
		}
		_ = m.saveState(state)
		return Result{Status: m.status(state)}, err
	}
	// Replacement can be asynchronous on Windows, and a Unix service still
	// needs to restart and pass its health check. Keep the version reported by
	// this running process authoritative until the new executable actually
	// starts. Otherwise a blocked helper can make an old binary claim that the
	// update is already installed.
	state.PendingVersion = release.TagName
	state.BlockedVersion = ""
	state.LastError = ""
	state.RetryAfter = time.Time{}
	state.Failures = 0
	state.UpdatedAt = time.Now().UTC()
	if err := m.saveState(state); err != nil {
		return Result{}, err
	}
	status := m.status(state)
	return Result{Status: status, Applied: true, RestartRequired: restart, TargetVersion: release.TagName}, nil
}

func (m *Manager) Run(ctx context.Context, notify func(Result, error)) {
	timer := time.NewTimer(90 * time.Second)
	defer timer.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-timer.C:
			result, err := m.Auto(ctx)
			if notify != nil {
				notify(result, err)
			}
			timer.Reset(time.Minute)
		}
	}
}

// ConfirmStartup completes a Unix update only after the new service responds
// with the expected version. A failed health check restores the prior binary.
func (m *Manager) ConfirmStartup(ctx context.Context) error {
	if !pendingUpdateFor(m.executable, m.currentVersion) {
		return nil
	}
	return m.ConfirmInstalled(ctx, m.currentVersion)
}

func (m *Manager) ConfirmInstalled(ctx context.Context, expectedVersion string) error {
	if !pendingUpdateFor(m.executable, expectedVersion) {
		return errors.New("updated executable has no pending rollback marker")
	}
	if m.healthURL == "" {
		select {
		case <-ctx.Done():
			return nil
		case <-time.After(15 * time.Second):
		}
		return clearPendingUpdate(m.executable)
	}
	client := &http.Client{Timeout: 3 * time.Second}
	deadline := time.NewTimer(45 * time.Second)
	defer deadline.Stop()
	probe := time.NewTicker(time.Second)
	defer probe.Stop()
	for {
		request, err := http.NewRequestWithContext(ctx, http.MethodGet, m.healthURL, nil)
		if err == nil {
			if response, callErr := client.Do(request); callErr == nil {
				var health struct {
					OK      bool   `json:"ok"`
					Version string `json:"version"`
				}
				decodeErr := json.NewDecoder(io.LimitReader(response.Body, 1<<20)).Decode(&health)
				_ = response.Body.Close()
				if response.StatusCode == http.StatusOK && decodeErr == nil && health.OK && health.Version == expectedVersion {
					return clearPendingUpdate(m.executable)
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil
		case <-deadline.C:
			if err := RollbackFailedStart(expectedVersion); err != nil {
				return fmt.Errorf("updated service failed health check and rollback failed: %w", err)
			}
			return errors.New("updated service failed health check; previous version restored")
		case <-probe.C:
		}
	}
}

func (m *Manager) status(state State) Status {
	enabled := m.settings.DefaultEnabled()
	// An omitted config value starts off but permits the local dashboard or
	// extension toggle to opt in. An explicit false remains an admin lock.
	if (m.settings.Enabled == nil || enabled) && state.EnabledOverride != nil {
		enabled = *state.EnabledOverride
	}
	return Status{
		Enabled: enabled, Channel: m.settings.Channel, Repository: m.settings.Repository,
		CurrentVersion: m.currentVersion, AvailableVersion: state.LastAvailable,
		UpdateAvailable: managedVersion(m.currentVersion) && newerVersion(m.currentVersion, state.LastAvailable),
		LastChecked:     state.LastChecked, LastInstalled: state.LastInstalled, PendingVersion: state.PendingVersion, LastError: state.LastError, BlockedVersion: state.BlockedVersion, RetryAfter: state.RetryAfter,
		ManagedBuild: managedVersion(m.currentVersion),
	}
}

func (m *Manager) latest(ctx context.Context) (Release, error) {
	path := "/releases/latest"
	if m.settings.Channel == "preview" {
		path = "/releases?per_page=20"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodGet, "https://api.github.com/repos/"+m.settings.Repository+path, nil)
	if err != nil {
		return Release{}, err
	}
	request.Header.Set("Accept", "application/vnd.github+json")
	request.Header.Set("User-Agent", "ContextBridge-Updater/"+m.currentVersion)
	response, err := m.client.Do(request)
	if err != nil {
		return Release{}, err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return Release{}, fmt.Errorf("release API returned %s", response.Status)
	}
	decoder := json.NewDecoder(io.LimitReader(response.Body, 8<<20))
	if m.settings.Channel == "preview" {
		var releases []Release
		if err := decoder.Decode(&releases); err != nil {
			return Release{}, err
		}
		for _, release := range releases {
			if !release.Draft {
				return release, nil
			}
		}
		return Release{}, errors.New("no release is available")
	}
	var release Release
	if err := decoder.Decode(&release); err != nil {
		return Release{}, err
	}
	if release.Draft || release.Prerelease || release.TagName == "" {
		return Release{}, errors.New("latest stable release is invalid")
	}
	return release, nil
}

func (m *Manager) install(ctx context.Context, release Release, automatic bool) (bool, error) {
	assetName := releaseAssetName(runtime.GOOS, runtime.GOARCH)
	asset, checksums, err := releaseAssets(release, assetName)
	if err != nil {
		return false, err
	}
	temporary, err := os.MkdirTemp(filepath.Dir(m.executable), ".contextbridge-update-")
	if err != nil {
		return false, err
	}
	defer os.RemoveAll(temporary)
	archivePath := filepath.Join(temporary, asset.Name)
	checksumPath := filepath.Join(temporary, "SHA256SUMS")
	if err := m.download(ctx, asset, archivePath); err != nil {
		return false, err
	}
	if checksums.Size > 1<<20 {
		return false, errors.New("release checksum file is too large")
	}
	if err := m.download(ctx, checksums, checksumPath); err != nil {
		return false, err
	}
	expected, err := checksumFor(checksumPath, asset.Name)
	if err != nil {
		return false, err
	}
	actual, err := fileSHA256(archivePath)
	if err != nil {
		return false, err
	}
	if !strings.EqualFold(expected, actual) {
		return false, errors.New("release checksum does not match SHA256SUMS")
	}
	if !strings.HasPrefix(asset.Digest, "sha256:") || !strings.EqualFold(strings.TrimPrefix(asset.Digest, "sha256:"), actual) {
		return false, errors.New("release checksum does not match the GitHub asset digest")
	}
	if digest, err := fileSHA256(checksumPath); err != nil || !strings.EqualFold(checksums.Digest, "sha256:"+digest) {
		return false, errors.New("SHA256SUMS does not match the GitHub asset digest")
	}
	staged := filepath.Join(temporary, executableName(runtime.GOOS))
	if err := extractExecutable(archivePath, staged); err != nil {
		return false, err
	}
	if err := os.Chmod(staged, 0755); err != nil && runtime.GOOS != "windows" {
		return false, err
	}
	if err := validateExecutable(staged, release.TagName, m.configPath); err != nil {
		return false, err
	}
	if automatic && !m.idle(ctx) {
		return false, errUpdateBusy
	}
	return replaceExecutable(m.executable, staged, release.TagName, m.configPath, m.healthURL, m.failurePath())
}

func (m *Manager) download(ctx context.Context, asset Asset, destination string) error {
	if asset.BrowserDownloadURL == "" || !strings.HasPrefix(asset.BrowserDownloadURL, "https://") {
		return fmt.Errorf("release asset %s has no secure download URL", asset.Name)
	}
	if asset.Size <= 0 || asset.Size > maxReleaseBytes {
		return fmt.Errorf("release asset %s has an invalid size", asset.Name)
	}
	request, _ := http.NewRequestWithContext(ctx, http.MethodGet, asset.BrowserDownloadURL, nil)
	request.Header.Set("User-Agent", "ContextBridge-Updater/"+m.currentVersion)
	response, err := m.client.Do(request)
	if err != nil {
		return err
	}
	defer response.Body.Close()
	if response.StatusCode != http.StatusOK {
		return fmt.Errorf("download %s returned %s", asset.Name, response.Status)
	}
	file, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(file, io.LimitReader(response.Body, maxReleaseBytes+1))
	closeErr := file.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written != asset.Size {
		return fmt.Errorf("download %s size mismatch", asset.Name)
	}
	return nil
}

func releaseAssets(release Release, wanted string) (Asset, Asset, error) {
	var archive, checksums Asset
	for _, asset := range release.Assets {
		switch asset.Name {
		case wanted:
			archive = asset
		case "SHA256SUMS":
			checksums = asset
		}
	}
	if archive.Name == "" || checksums.Name == "" {
		return Asset{}, Asset{}, fmt.Errorf("release %s does not contain %s and SHA256SUMS", release.TagName, wanted)
	}
	return archive, checksums, nil
}

func releaseAssetName(goos, goarch string) string {
	extension := ".tar.gz"
	if goos == "windows" {
		extension = ".zip"
	}
	return "contextbridge_" + goos + "_" + goarch + extension
}

func executableName(goos string) string {
	if goos == "windows" {
		return "contextbridge.exe"
	}
	return "contextbridge"
}

func extractExecutable(archivePath, destination string) error {
	if strings.HasSuffix(archivePath, ".zip") {
		archive, err := zip.OpenReader(archivePath)
		if err != nil {
			return err
		}
		defer archive.Close()
		for _, file := range archive.File {
			if filepath.Base(file.Name) != "contextbridge.exe" || file.FileInfo().IsDir() {
				continue
			}
			if file.UncompressedSize64 > 128<<20 {
				return errors.New("executable in release archive is too large")
			}
			input, err := file.Open()
			if err != nil {
				return err
			}
			err = writeExecutable(input, destination)
			input.Close()
			return err
		}
		return errors.New("release archive does not contain contextbridge.exe")
	}
	archive, err := os.Open(archivePath)
	if err != nil {
		return err
	}
	defer archive.Close()
	compressed, err := gzip.NewReader(archive)
	if err != nil {
		return err
	}
	defer compressed.Close()
	reader := tar.NewReader(compressed)
	for {
		header, err := reader.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if filepath.Base(header.Name) != "contextbridge" || header.Typeflag != tar.TypeReg {
			continue
		}
		if header.Size <= 0 || header.Size > 128<<20 {
			return errors.New("executable in release archive has an invalid size")
		}
		return writeExecutable(io.LimitReader(reader, header.Size), destination)
	}
	return errors.New("release archive does not contain contextbridge")
}

func writeExecutable(input io.Reader, destination string) error {
	output, err := os.OpenFile(destination, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0700)
	if err != nil {
		return err
	}
	written, copyErr := io.Copy(output, io.LimitReader(input, 128<<20+1))
	closeErr := output.Close()
	if copyErr != nil {
		return copyErr
	}
	if closeErr != nil {
		return closeErr
	}
	if written <= 0 || written > 128<<20 {
		return errors.New("extracted executable has an invalid size")
	}
	return nil
}

func validateExecutable(path, expected, configPath string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	output, err := exec.CommandContext(ctx, path, "version").CombinedOutput()
	if err != nil {
		return fmt.Errorf("staged executable did not start: %w", err)
	}
	if strings.TrimSpace(string(output)) != expected {
		return fmt.Errorf("staged executable reports %q instead of %q", strings.TrimSpace(string(output)), expected)
	}
	if configPath != "" {
		checkCtx, checkCancel := context.WithTimeout(context.Background(), 15*time.Second)
		defer checkCancel()
		if output, err := exec.CommandContext(checkCtx, path, "update", "self-test", "--config", configPath).CombinedOutput(); err != nil {
			return fmt.Errorf("staged executable rejected the current configuration: %s: %w", strings.TrimSpace(string(output)), err)
		}
	}
	return nil
}

func checksumFor(path, asset string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	for _, line := range strings.Split(string(raw), "\n") {
		fields := strings.Fields(line)
		if len(fields) >= 2 && filepath.Base(strings.TrimPrefix(fields[len(fields)-1], "*")) == asset {
			value := strings.ToLower(fields[0])
			if len(value) == 64 {
				if _, err := hex.DecodeString(value); err == nil {
					return value, nil
				}
			}
		}
	}
	return "", fmt.Errorf("SHA256SUMS does not contain %s", asset)
}

func fileSHA256(path string) (string, error) {
	file, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer file.Close()
	hash := sha256.New()
	if _, err := io.Copy(hash, file); err != nil {
		return "", err
	}
	return hex.EncodeToString(hash.Sum(nil)), nil
}

func (m *Manager) statePath() string   { return filepath.Join(m.dataDir, "update-state.json") }
func (m *Manager) failurePath() string { return filepath.Join(m.dataDir, "update-failed.json") }

func (m *Manager) acquireLock(ctx context.Context) (func(), error) {
	path := filepath.Join(m.dataDir, "update.lock")
	for {
		file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
		if err == nil {
			_, _ = fmt.Fprintf(file, "%d\n%s\n", os.Getpid(), time.Now().UTC().Format(time.RFC3339))
			_ = file.Close()
			return func() { _ = os.Remove(path) }, nil
		}
		if !errors.Is(err, os.ErrExist) {
			return nil, err
		}
		if info, statErr := os.Stat(path); statErr == nil {
			contents, readErr := os.ReadFile(path)
			owner, parseErr := strconv.Atoi(strings.TrimSpace(strings.SplitN(string(contents), "\n", 2)[0]))
			// The Windows replacement helper can briefly outlive its Go owner.
			// Give it time to finish before reclaiming an abandoned lock.
			orphaned := readErr == nil && parseErr == nil && owner > 0 && !lockProcessAlive(owner) && time.Since(info.ModTime()) > 3*time.Minute
			expired := time.Since(info.ModTime()) > 15*time.Minute
			if orphaned || expired {
				// Recheck identity so a newly replaced lock is not mistaken for
				// the stale file that was just inspected.
				if current, checkErr := os.Stat(path); checkErr == nil && os.SameFile(info, current) {
					if os.Remove(path) == nil {
						continue
					}
				}
			}
		}
		select {
		case <-ctx.Done():
			return nil, fmt.Errorf("another update operation is active: %w", ctx.Err())
		case <-time.After(200 * time.Millisecond):
		}
	}
}

func (m *Manager) loadState() (State, error) {
	raw, err := os.ReadFile(m.statePath())
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return State{}, err
	}
	var state State
	if err == nil {
		if err := json.Unmarshal(raw, &state); err != nil {
			return State{}, fmt.Errorf("parse update state: %w", err)
		}
	}
	// The version of the process reading this state is the authoritative
	// installed version. A prior process may only have staged a helper.
	if managedVersion(m.currentVersion) {
		state.LastInstalled = m.currentVersion
		if state.PendingVersion == m.currentVersion || newerVersion(state.PendingVersion, m.currentVersion) {
			state.PendingVersion = ""
		}
	}
	if failed, readErr := os.ReadFile(m.failurePath()); readErr == nil {
		var marker struct {
			Version string `json:"version"`
			Error   string `json:"error"`
		}
		if json.Unmarshal(failed, &marker) == nil && marker.Version != "" {
			if marker.Version == m.currentVersion || newerVersion(marker.Version, m.currentVersion) {
				// A later healthy release supersedes an older rollback marker.
				// Equality also proves that the previously targeted executable is
				// now running; the asynchronous failure marker is stale.
				// Keep the marker on disk for diagnosis, but do not show its
				// failure as the current installation's status.
				state.BlockedVersion = ""
				if state.LastError == marker.Error {
					state.LastError = ""
				}
				if state.PendingVersion == marker.Version {
					state.PendingVersion = ""
				}
			} else {
				state.BlockedVersion = marker.Version
				state.LastError = marker.Error
				if state.PendingVersion == marker.Version {
					state.PendingVersion = ""
				}
			}
		}
	}
	return state, nil
}

func (m *Manager) saveState(state State) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	temporary := m.statePath() + ".tmp"
	if err := os.WriteFile(temporary, append(raw, '\n'), 0600); err != nil {
		return err
	}
	return os.Rename(temporary, m.statePath())
}

func managedVersion(value string) bool {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	parts := strings.SplitN(value, "-", 2)
	numbers := strings.Split(parts[0], ".")
	if len(numbers) != 3 {
		return false
	}
	for _, number := range numbers {
		if _, err := strconv.Atoi(number); err != nil {
			return false
		}
	}
	return true
}

func newerVersion(current, candidate string) bool {
	left, okLeft := versionParts(current)
	right, okRight := versionParts(candidate)
	if !okLeft || !okRight {
		return false
	}
	for index := range left {
		if right[index] != left[index] {
			return right[index] > left[index]
		}
	}
	return false
}

func versionParts(value string) ([3]int, bool) {
	var output [3]int
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	numbers := strings.Split(strings.SplitN(value, "-", 2)[0], ".")
	if len(numbers) != 3 {
		return output, false
	}
	for index, number := range numbers {
		parsed, err := strconv.Atoi(number)
		if err != nil || parsed < 0 {
			return output, false
		}
		output[index] = parsed
	}
	return output, true
}

func safeRepositoryPart(value string) bool {
	if value == "" || len(value) > 128 || strings.Contains(value, "..") {
		return false
	}
	for _, char := range value {
		if (char >= 'a' && char <= 'z') || (char >= 'A' && char <= 'Z') || (char >= '0' && char <= '9') || char == '-' || char == '_' || char == '.' {
			continue
		}
		return false
	}
	return true
}

func cleanError(err error) string {
	value := strings.TrimSpace(err.Error())
	if len(value) > 500 {
		value = value[:500]
	}
	return value
}

func scheduleRetry(state *State) {
	state.Failures = min(state.Failures+1, 8)
	delay := 15 * time.Minute * time.Duration(1<<(state.Failures-1))
	if delay > 24*time.Hour {
		delay = 24 * time.Hour
	}
	state.RetryAfter = time.Now().UTC().Add(delay)
}

package sandbox

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"time"
)

// microVMWorkspace is where the project appears in the guest.
const microVMWorkspace = "/workspace"

// MicroVMConfig configures the microVM backend. See docs/adr/0003-microvm-environment.md.
type MicroVMConfig struct {
	// BinaryPath is the msb executable. Required. The embedder provides it.
	BinaryPath string

	// Home holds msb's images, sandboxes and volumes, and this backend's records.
	// Required: a default under the user's profile would mix with a separate install.
	Home string

	// Image is the OCI image of the environment. Empty means DefaultMicroVMImage.
	Image string

	// CPUs and MemoryMB size each sandbox. Zero means the defaults below.
	CPUs     int
	MemoryMB int

	// Ports are msb port mappings published when a sandbox is created. Nil means
	// DefaultMicroVMPorts: a command does not announce its port, so they are set ahead.
	Ports []string

	// Shell runs a command in the guest. Empty means bash, which the default image has.
	Shell []string

	// Env is added to every command. The host's environment is never passed in.
	Env map[string]string
}

const (
	// DefaultMicroVMImage has git, python, a compiler and node. Smaller images lack them,
	// and an agent would spend its first turns installing them.
	DefaultMicroVMImage = "node:22-bookworm"

	defaultMicroVMCPUs     = 4
	defaultMicroVMMemoryMB = 2048
)

// DefaultMicroVMPorts covers the usual dev servers.
var DefaultMicroVMPorts = []string{"3000-3003:3000-3003", "4200:4200", "4321:4321", "5000:5000", "5173-5175:5173-5175", "8000:8000", "8080:8080"}

// microVMWatcherEnv makes file watchers poll: edits made on the host do not reach a watcher
// in the guest, and the file tools write on the host.
var microVMWatcherEnv = map[string]string{"CHOKIDAR_USEPOLLING": "true", "CHOKIDAR_INTERVAL": "300", "WATCHPACK_POLLING": "300"}

// microVMVolumes names what a package manager installs, by the file that announces it. In the
// mount an install is 8 times slower, and it leaves Linux binaries in the user's folder.
var microVMVolumes = []struct{ marker, dir string }{
	{"package.json", "node_modules"},
	{"requirements.txt", ".venv"},
	{"pyproject.toml", ".venv"},
	{"Cargo.toml", "target"},
}

func (cfg MicroVMConfig) withDefaults() MicroVMConfig {
	if cfg.Image == "" {
		cfg.Image = DefaultMicroVMImage
	}
	if cfg.CPUs <= 0 {
		cfg.CPUs = defaultMicroVMCPUs
	}
	if cfg.MemoryMB <= 0 {
		cfg.MemoryMB = defaultMicroVMMemoryMB
	}
	if cfg.Ports == nil {
		cfg.Ports = DefaultMicroVMPorts
	}
	if len(cfg.Shell) == 0 {
		cfg.Shell = []string{"bash", "-c"}
	}
	return cfg
}

// microVMInstance is one project's sandbox, shared by every executor in this process: an
// embedder may hold one client per model, and a command must not lose its sandbox because
// another client closed.
type microVMInstance struct {
	mu   sync.Mutex
	refs int
}

var (
	microVMInstances   = map[string]*microVMInstance{}
	microVMInstancesMu sync.Mutex
)

func microVMInstanceFor(name string) *microVMInstance {
	microVMInstancesMu.Lock()
	defer microVMInstancesMu.Unlock()
	instance := microVMInstances[name]
	if instance == nil {
		instance = &microVMInstance{}
		microVMInstances[name] = instance
	}
	return instance
}

// MicroVMExecutor runs commands in a microVM per project through the msb command line.
type MicroVMExecutor struct {
	cfg MicroVMConfig

	mu     sync.Mutex
	closed bool
	// roots are the projects this executor holds a reference on, by sandbox name.
	roots map[string]string
}

// NewMicroVMExecutor checks the configuration. It starts nothing.
func NewMicroVMExecutor(cfg MicroVMConfig) (*MicroVMExecutor, error) {
	if cfg.BinaryPath == "" {
		return nil, errors.New("microvm: the msb binary path is required")
	}
	if cfg.Home == "" {
		return nil, errors.New("microvm: the home folder is required")
	}
	return &MicroVMExecutor{cfg: cfg.withDefaults(), roots: map[string]string{}}, nil
}

// Kind implements Executor.
func (e *MicroVMExecutor) Kind() EnvironmentKind { return EnvironmentMicroVM }

// Healthy implements Executor: msb reports whether this host can run a sandbox.
func (e *MicroVMExecutor) Healthy(ctx context.Context) error {
	if _, err := os.Stat(e.cfg.BinaryPath); err != nil {
		return fmt.Errorf("microvm: %w", err)
	}
	if out, err := e.msb(ctx, "doctor"); err != nil {
		return fmt.Errorf("microvm: this host cannot run a sandbox: %w: %s", err, lastLine(out))
	}
	return nil
}

// Close stops the sandboxes no other executor is using. A stopped sandbox keeps its disk,
// so the next session starts in a second.
func (e *MicroVMExecutor) Close() error {
	e.mu.Lock()
	if e.closed {
		e.mu.Unlock()
		return nil
	}
	e.closed = true
	roots := e.roots
	e.roots = map[string]string{}
	e.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), time.Minute)
	defer cancel()
	var errs []error
	for name := range roots {
		instance := microVMInstanceFor(name)
		instance.mu.Lock()
		instance.refs--
		if instance.refs <= 0 {
			if out, err := e.msb(ctx, "stop", name); err != nil {
				errs = append(errs, fmt.Errorf("microvm: stop %s: %w: %s", name, err, lastLine(out)))
			}
		}
		instance.mu.Unlock()
	}
	return errors.Join(errs...)
}

// BackgroundExecutor returns an executor for one background task. It shares this executor's
// sandboxes and leaves them running when the task ends. A task needs no sandbox of its own:
// unlike the sidecar, a sandbox runs commands side by side.
func (e *MicroVMExecutor) BackgroundExecutor() Executor { return microVMTask{e} }

type microVMTask struct{ *MicroVMExecutor }

func (microVMTask) Close() error                  { return nil }
func (microVMTask) Healthy(context.Context) error { return nil }

// Run implements Executor.
func (e *MicroVMExecutor) Run(ctx context.Context, req RunRequest) (RunResult, error) {
	if req.Stdin != nil {
		return RunResult{}, errors.New("microvm: stdin is not supported by this backend yet")
	}
	root, name, err := e.rootFor(req.WorkDir)
	if err != nil {
		return RunResult{}, err
	}
	if err := e.ensure(ctx, root, name); err != nil {
		return RunResult{}, err
	}
	guestDir, err := microVMGuestDir(root, req.WorkDir)
	if err != nil {
		return RunResult{}, err
	}

	shell := req.Shell
	if len(shell) == 0 {
		shell = e.cfg.Shell
	}
	// Without --stream, msb hands output over only when the command ends, and a dev server's
	// address and errors would never reach the agent.
	args := []string{"exec", "--stream", "--no-stdin", "-w", guestDir}
	for _, pair := range microVMEnv(e.cfg.Env, req.Env) {
		args = append(args, "-e", pair)
	}
	args = append(args, name, "--")
	args = append(args, shell...)
	args = append(args, req.Command)

	runCtx := ctx
	if req.Timeout > 0 {
		var cancel context.CancelFunc
		runCtx, cancel = context.WithTimeout(ctx, req.Timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(runCtx, e.cfg.BinaryPath, args...)
	cmd.Env = e.environment()
	cmd.WaitDelay = 5 * time.Second
	var stdout, stderr bytes.Buffer
	var outputMu sync.Mutex
	cmd.Stdout = &microVMStream{mu: &outputMu, buffer: &stdout, stream: sandboxdStreamStdout, onOutput: req.OnOutput}
	cmd.Stderr = &microVMStream{mu: &outputMu, buffer: &stderr, stream: sandboxdStreamStderr, onOutput: req.OnOutput}

	started := time.Now()
	runErr := cmd.Run()
	result := RunResult{Stdout: stdout.String(), Stderr: stderr.String(), Duration: time.Since(started)}
	if runCtx.Err() != nil {
		result.ExitCode = -1
		return result, runCtx.Err()
	}
	var exit *exec.ExitError
	switch {
	case errors.As(runErr, &exit):
		result.ExitCode = exit.ExitCode()
	case runErr != nil:
		result.ExitCode = -1
		return result, fmt.Errorf("microvm: run msb: %w", runErr)
	}
	return result, nil
}

// microVMStream keeps output and hands it over as it comes. os/exec copies each stream from
// its own goroutine, and a caller of OnOutput expects one call at a time, as with the sidecar.
type microVMStream struct {
	mu       *sync.Mutex
	buffer   *bytes.Buffer
	stream   string
	onOutput func(string, []byte)
}

func (s *microVMStream) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.buffer.Write(p)
	if s.onOutput != nil {
		s.onOutput(s.stream, p)
	}
	return len(p), nil
}

// rootFor finds the project a working directory belongs to: one this executor already holds,
// or the directory itself. It takes a reference on a new one.
func (e *MicroVMExecutor) rootFor(workDir string) (root, name string, err error) {
	if strings.TrimSpace(workDir) == "" {
		return "", "", errors.New("microvm: a command needs a working directory")
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", "", fmt.Errorf("microvm: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	if e.closed {
		return "", "", errors.New("microvm: executor is closed")
	}
	for held, heldRoot := range e.roots {
		if _, err := microVMGuestDir(heldRoot, abs); err == nil {
			return heldRoot, held, nil
		}
	}
	if microVMTooWide(abs) {
		return "", "", fmt.Errorf("microvm: cannot mount %s: it holds a whole drive or the user's profile, so nothing would be kept from the sandbox. Open a project folder", abs)
	}
	name = microVMName(abs)
	instance := microVMInstanceFor(name)
	instance.mu.Lock()
	instance.refs++
	instance.mu.Unlock()
	e.roots[name] = abs
	return abs, name, nil
}

// microVMGuestDir maps a folder of the project to its place in the guest.
func microVMGuestDir(root, dir string) (string, error) {
	abs, err := filepath.Abs(dir)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(root, abs)
	if err != nil {
		return "", err
	}
	if rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("microvm: %s is outside the project %s", abs, root)
	}
	if rel == "." {
		return microVMWorkspace, nil
	}
	return microVMWorkspace + "/" + filepath.ToSlash(rel), nil
}

// microVMName is stable for a folder, so a later session finds its sandbox.
func microVMName(root string) string {
	key := filepath.Clean(root)
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	sum := sha256.Sum256([]byte(key))
	return "seshat-" + hex.EncodeToString(sum[:])[:12]
}

// microVMTooWide says whether mounting dir would hand the guest a drive or the user's profile.
func microVMTooWide(dir string) bool {
	clean := filepath.Clean(dir)
	if filepath.Dir(clean) == clean {
		return true
	}
	home, err := os.UserHomeDir()
	if err != nil || home == "" {
		return false
	}
	if rel, err := filepath.Rel(clean, home); err == nil {
		return rel == "." || (rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)))
	}
	return false
}

// microVMEnv lists what a command gets: the watcher settings, then the embedder's, then the
// request's. The order is fixed so a command line can be read in a log.
func microVMEnv(layers ...map[string]string) []string {
	merged := map[string]string{}
	for key, value := range microVMWatcherEnv {
		merged[key] = value
	}
	for _, layer := range layers {
		for key, value := range layer {
			merged[key] = value
		}
	}
	pairs := make([]string, 0, len(merged))
	for key, value := range merged {
		pairs = append(pairs, key+"="+value)
	}
	slices.Sort(pairs)
	return pairs
}

// microVMRecord is what a sandbox was created with. msb cannot add a volume to a sandbox, so
// a volume missing from the record means it has to be replaced.
type microVMRecord struct {
	Root    string   `json:"root"`
	Volumes []string `json:"volumes"`
}

func (e *MicroVMExecutor) recordPath(name string) string {
	return filepath.Join(e.cfg.Home, "seshat-sandboxes", name+".json")
}

func (e *MicroVMExecutor) readRecord(name string) (microVMRecord, bool) {
	raw, err := os.ReadFile(e.recordPath(name))
	if err != nil {
		return microVMRecord{}, false
	}
	var record microVMRecord
	if err := json.Unmarshal(raw, &record); err != nil {
		return microVMRecord{}, false
	}
	return record, true
}

func (e *MicroVMExecutor) writeRecord(name string, record microVMRecord) error {
	path := e.recordPath(name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	raw, err := json.Marshal(record)
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o644)
}

// microVMWantedVolumes lists the volumes a project's files call for.
func microVMWantedVolumes(root string) []string {
	var dirs []string
	for _, volume := range microVMVolumes {
		if _, err := os.Stat(filepath.Join(root, volume.marker)); err == nil && !slices.Contains(dirs, volume.dir) {
			dirs = append(dirs, volume.dir)
		}
	}
	slices.Sort(dirs)
	return dirs
}

// ensure leaves the project's sandbox running, with the volumes its files call for.
func (e *MicroVMExecutor) ensure(ctx context.Context, root, name string) error {
	instance := microVMInstanceFor(name)
	instance.mu.Lock()
	defer instance.mu.Unlock()

	status, exists := e.status(ctx, name)
	record, recorded := e.readRecord(name)
	volumes := microVMWantedVolumes(root)
	for _, dir := range record.Volumes {
		if !slices.Contains(volumes, dir) {
			volumes = append(volumes, dir)
		}
	}
	slices.Sort(volumes)

	if exists && (!recorded || !slices.Equal(volumes, record.Volumes)) {
		if out, err := e.msb(ctx, "rm", "-f", name); err != nil {
			return fmt.Errorf("microvm: replace %s: %w: %s", name, err, lastLine(out))
		}
		exists = false
	}
	if !exists {
		return e.create(ctx, root, name, volumes)
	}
	if status != "Running" {
		if out, err := e.msb(ctx, "start", name); err != nil {
			return fmt.Errorf("microvm: start %s: %w: %s", name, err, lastLine(out))
		}
	}
	return nil
}

func (e *MicroVMExecutor) status(ctx context.Context, name string) (string, bool) {
	out, err := e.msb(ctx, "inspect", "--format", "json", name)
	if err != nil {
		return "", false
	}
	var info struct {
		Status string `json:"status"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return "", false
	}
	return info.Status, true
}

func (e *MicroVMExecutor) create(ctx context.Context, root, name string, volumes []string) error {
	args := []string{"create", "--name", name, "-c", fmt.Sprint(e.cfg.CPUs), "-m", fmt.Sprintf("%dM", e.cfg.MemoryMB),
		"-w", microVMWorkspace, "-v", root + ":" + microVMWorkspace}
	for _, dir := range volumes {
		volume := microVMVolumeName(name, dir)
		if out, err := e.msb(ctx, "volume", "create", volume); err != nil && !strings.Contains(string(out), "already exists") {
			return fmt.Errorf("microvm: create volume %s: %w: %s", volume, err, lastLine(out))
		}
		args = append(args, "-v", volume+":"+microVMWorkspace+"/"+dir)
	}
	for _, port := range e.cfg.Ports {
		args = append(args, "-p", port)
	}
	args = append(args, e.cfg.Image)
	if out, err := e.msb(ctx, args...); err != nil {
		return fmt.Errorf("microvm: create %s: %w: %s", name, err, lastLine(out))
	}
	if err := e.writeRecord(name, microVMRecord{Root: root, Volumes: volumes}); err != nil {
		return fmt.Errorf("microvm: %w", err)
	}
	return nil
}

func microVMVolumeName(name, dir string) string {
	return name + "-" + strings.TrimPrefix(dir, ".")
}

// environment is what msb itself runs with. The private DOCKER_CONFIG keeps it away from the
// host's Docker credential store, which made pulls of public images fail.
func (e *MicroVMExecutor) environment() []string {
	dockerConfig := filepath.Join(e.cfg.Home, "docker-config")
	_ = os.MkdirAll(dockerConfig, 0o755)
	return append(os.Environ(), "MSB_HOME="+e.cfg.Home, "DOCKER_CONFIG="+dockerConfig)
}

func (e *MicroVMExecutor) msb(ctx context.Context, args ...string) ([]byte, error) {
	cmd := exec.CommandContext(ctx, e.cfg.BinaryPath, args...)
	cmd.Env = e.environment()
	cmd.WaitDelay = 5 * time.Second
	return cmd.CombinedOutput()
}

func lastLine(out []byte) string {
	lines := strings.Split(strings.TrimSpace(string(out)), "\n")
	return strings.TrimSpace(lines[len(lines)-1])
}

// PrepareMicroVM checks that this host can run a sandbox and pulls the image. A pull takes
// minutes, so it belongs where the user turned the mode on and not inside a command's
// timeout. onOutput, when set, receives msb's progress.
func PrepareMicroVM(ctx context.Context, cfg MicroVMConfig, onOutput func(string)) error {
	e, err := NewMicroVMExecutor(cfg)
	if err != nil {
		return err
	}
	if err := e.Healthy(ctx); err != nil {
		return err
	}
	cmd := exec.CommandContext(ctx, e.cfg.BinaryPath, "pull", e.cfg.Image)
	cmd.Env = e.environment()
	cmd.WaitDelay = 5 * time.Second
	var tail bytes.Buffer
	write := func(p []byte) {
		tail.Write(p)
		if onOutput != nil {
			onOutput(string(p))
		}
	}
	cmd.Stdout = writerFunc(write)
	cmd.Stderr = writerFunc(write)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("microvm: pull %s: %w: %s", e.cfg.Image, err, lastLine(tail.Bytes()))
	}
	return nil
}

type writerFunc func([]byte)

func (f writerFunc) Write(p []byte) (int, error) {
	f(p)
	return len(p), nil
}

// StopMicroVMEnvironments stops every sandbox of this backend. A sandbox outlives the process
// that started it, so an embedder calls this at startup in case the last run did not close.
func StopMicroVMEnvironments(ctx context.Context, cfg MicroVMConfig) error {
	return forEachMicroVM(cfg, func(e *MicroVMExecutor, name string, _ microVMRecord) error {
		if status, exists := e.status(ctx, name); !exists || status != "Running" {
			return nil
		}
		out, err := e.msb(ctx, "stop", name)
		if err != nil {
			return fmt.Errorf("microvm: stop %s: %w: %s", name, err, lastLine(out))
		}
		return nil
	})
}

// RemoveMicroVMEnvironments deletes every sandbox and volume of this backend, which is how an
// embedder gives the disk back. The projects' folders are not touched.
func RemoveMicroVMEnvironments(ctx context.Context, cfg MicroVMConfig) error {
	return forEachMicroVM(cfg, func(e *MicroVMExecutor, name string, record microVMRecord) error {
		if _, exists := e.status(ctx, name); exists {
			if out, err := e.msb(ctx, "rm", "-f", name); err != nil {
				return fmt.Errorf("microvm: remove %s: %w: %s", name, err, lastLine(out))
			}
		}
		for _, dir := range record.Volumes {
			volume := microVMVolumeName(name, dir)
			if out, err := e.msb(ctx, "volume", "rm", volume); err != nil {
				return fmt.Errorf("microvm: remove volume %s: %w: %s", volume, err, lastLine(out))
			}
		}
		return os.Remove(e.recordPath(name))
	})
}

func forEachMicroVM(cfg MicroVMConfig, do func(*MicroVMExecutor, string, microVMRecord) error) error {
	e, err := NewMicroVMExecutor(cfg)
	if err != nil {
		return err
	}
	entries, err := os.ReadDir(filepath.Join(e.cfg.Home, "seshat-sandboxes"))
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("microvm: %w", err)
	}
	var errs []error
	for _, entry := range entries {
		name, isRecord := strings.CutSuffix(entry.Name(), ".json")
		if !isRecord {
			continue
		}
		record, _ := e.readRecord(name)
		if err := do(e, name, record); err != nil {
			errs = append(errs, err)
		}
	}
	return errors.Join(errs...)
}

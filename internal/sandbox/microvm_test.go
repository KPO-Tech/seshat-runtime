package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

// A fake msb: this test binary, run again, keeping its state in a folder and logging each call.
const fakeMSBStateEnv = "SESHAT_FAKE_MSB_STATE"

func runFakeMSB(state string) int {
	args := os.Args[1:]
	logFile, _ := os.OpenFile(filepath.Join(state, "calls.log"), os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	fmt.Fprintln(logFile, strings.Join(args, "\x1f"))
	logFile.Close()
	fail := func(message string) int {
		fmt.Fprintln(os.Stderr, "error: "+message)
		return 1
	}
	marker := func(name string) string { return filepath.Join(state, "sandbox-"+name) }

	if len(args) == 0 {
		return 2
	}
	switch args[0] {
	case "doctor":
		if _, err := os.Stat(filepath.Join(state, "no-hypervisor")); err == nil {
			return fail("no hypervisor")
		}
		fmt.Println("Host setup is ready.")
	case "pull":
		fmt.Println("pulling " + args[1])
	case "inspect":
		name := args[len(args)-1]
		raw, err := os.ReadFile(marker(name))
		if err != nil {
			return fail("sandbox not found: " + name)
		}
		fmt.Printf(`{"name":%q,"status":%q}`, name, string(raw))
	case "create":
		name := args[slices.Index(args, "--name")+1]
		if _, err := os.Stat(marker(name)); err == nil {
			return fail("sandbox already exists")
		}
		os.WriteFile(marker(name), []byte("Running"), 0o644)
	case "start":
		raw, err := os.ReadFile(marker(args[1]))
		if err != nil {
			return fail("not found")
		}
		if string(raw) == "Running" {
			return fail("already running")
		}
		os.WriteFile(marker(args[1]), []byte("Running"), 0o644)
	case "stop":
		os.WriteFile(marker(args[1]), []byte("Stopped"), 0o644)
	case "rm":
		os.Remove(marker(args[len(args)-1]))
	case "volume":
		volume := filepath.Join(state, "volume-"+args[2])
		if args[1] == "create" {
			if _, err := os.Stat(volume); err == nil {
				return fail("volume already exists: " + args[2])
			}
			os.WriteFile(volume, nil, 0o644)
		} else {
			os.Remove(volume)
		}
	case "exec":
		command := args[len(args)-1]
		fmt.Println("out:" + os.Getenv("MSB_HOME"))
		fmt.Fprintln(os.Stderr, "err:"+os.Getenv("DOCKER_CONFIG"))
		switch command {
		case "exit7":
			return 7
		case "hang":
			time.Sleep(time.Minute)
		}
	default:
		return 2
	}
	return 0
}

type fakeMSB struct {
	t     *testing.T
	state string
	cfg   MicroVMConfig
}

func newFakeMSB(t *testing.T) *fakeMSB {
	t.Helper()
	exe, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	state := t.TempDir()
	t.Setenv(fakeMSBStateEnv, state)
	return &fakeMSB{t: t, state: state, cfg: MicroVMConfig{BinaryPath: exe, Home: t.TempDir()}}
}

func (f *fakeMSB) executor() *MicroVMExecutor {
	f.t.Helper()
	e, err := NewMicroVMExecutor(f.cfg)
	if err != nil {
		f.t.Fatal(err)
	}
	f.t.Cleanup(func() { _ = e.Close() })
	return e
}

// calls returns the logged calls that start with the given word.
func (f *fakeMSB) calls(first string) [][]string {
	raw, _ := os.ReadFile(filepath.Join(f.state, "calls.log"))
	var found [][]string
	for _, line := range strings.Split(strings.TrimSpace(string(raw)), "\n") {
		parts := strings.Split(line, "\x1f")
		if parts[0] == first {
			found = append(found, parts)
		}
	}
	return found
}

func (f *fakeMSB) status(name string) string {
	raw, err := os.ReadFile(filepath.Join(f.state, "sandbox-"+name))
	if err != nil {
		return ""
	}
	return string(raw)
}

func TestMicroVMFirstCommandCreatesTheSandboxAndMountsTheProject(t *testing.T) {
	fake := newFakeMSB(t)
	project := t.TempDir()
	e := fake.executor()

	var streamed []string
	result, err := e.Run(context.Background(), RunRequest{
		Command: "echo hi", WorkDir: project,
		OnOutput: func(stream string, data []byte) { streamed = append(streamed, stream) },
	})
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 0 || !strings.Contains(result.Stdout, "out:") {
		t.Fatalf("result %+v", result)
	}
	if !slices.Contains(streamed, "stdout") || !slices.Contains(streamed, "stderr") {
		t.Fatalf("output must stream while the command runs: %v", streamed)
	}

	creates := fake.calls("create")
	if len(creates) != 1 {
		t.Fatalf("creates %v", creates)
	}
	create := creates[0]
	if !slices.Contains(create, project+":/workspace") {
		t.Fatalf("the project is not mounted: %v", create)
	}
	if !slices.Contains(create, "5173-5175:5173-5175") || create[len(create)-1] != DefaultMicroVMImage {
		t.Fatalf("defaults are missing: %v", create)
	}

	// A second command finds it running.
	if _, err := e.Run(context.Background(), RunRequest{Command: "echo again", WorkDir: project}); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls("create")) != 1 || len(fake.calls("start")) != 0 {
		t.Fatalf("a running sandbox must be used as it is: %v %v", fake.calls("create"), fake.calls("start"))
	}
}

func TestMicroVMCommandsGetTheWatcherSettingsAndNotTheHostEnvironment(t *testing.T) {
	fake := newFakeMSB(t)
	fake.cfg.Env = map[string]string{"FROM_EMBEDDER": "1"}
	t.Setenv("SECRET_ON_THE_HOST", "hidden")
	e := fake.executor()
	result, err := e.Run(context.Background(), RunRequest{Command: "x", WorkDir: t.TempDir(), Env: map[string]string{"FROM_REQUEST": "2"}})
	if err != nil {
		t.Fatal(err)
	}

	call := strings.Join(fake.calls("exec")[0], " ")
	for _, want := range []string{"CHOKIDAR_USEPOLLING=true", "WATCHPACK_POLLING=300", "FROM_EMBEDDER=1", "FROM_REQUEST=2", "--no-stdin", "--stream"} {
		if !strings.Contains(call, want) {
			t.Errorf("%q is not in %s", want, call)
		}
	}
	if strings.Contains(call, "SECRET_ON_THE_HOST") {
		t.Fatal("the host's environment reached the guest")
	}
	// msb itself is pointed at its own home and its own Docker config.
	if !strings.Contains(result.Stdout, fake.cfg.Home) || !strings.Contains(result.Stderr, filepath.Join(fake.cfg.Home, "docker-config")) {
		t.Fatalf("stdout %q stderr %q", result.Stdout, result.Stderr)
	}
}

func TestMicroVMExitCodesAndTimeouts(t *testing.T) {
	fake := newFakeMSB(t)
	e := fake.executor()
	project := t.TempDir()
	result, err := e.Run(context.Background(), RunRequest{Command: "exit7", WorkDir: project})
	if err != nil || result.ExitCode != 7 {
		t.Fatalf("result %+v err %v", result, err)
	}
	started := time.Now()
	_, err = e.Run(context.Background(), RunRequest{Command: "hang", WorkDir: project, Timeout: 300 * time.Millisecond})
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("err %v", err)
	}
	if time.Since(started) > 30*time.Second {
		t.Fatal("the timeout did not end the command")
	}
}

func TestMicroVMASubfolderUsesTheSameSandbox(t *testing.T) {
	fake := newFakeMSB(t)
	project := t.TempDir()
	sub := filepath.Join(project, "packages", "web")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatal(err)
	}
	e := fake.executor()
	for _, dir := range []string{project, sub} {
		if _, err := e.Run(context.Background(), RunRequest{Command: "x", WorkDir: dir}); err != nil {
			t.Fatal(err)
		}
	}
	if len(fake.calls("create")) != 1 {
		t.Fatalf("creates %v", fake.calls("create"))
	}
	execs := fake.calls("exec")
	if !slices.Contains(execs[1], "/workspace/packages/web") {
		t.Fatalf("the guest folder is wrong: %v", execs[1])
	}
}

func TestMicroVMRefusesADriveOrTheProfile(t *testing.T) {
	fake := newFakeMSB(t)
	e := fake.executor()
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	for _, dir := range []string{home, filepath.Dir(home), filepath.VolumeName(home) + string(filepath.Separator)} {
		if _, err := e.Run(context.Background(), RunRequest{Command: "x", WorkDir: dir}); err == nil || !strings.Contains(err.Error(), "project folder") {
			t.Errorf("%s: %v", dir, err)
		}
	}
	if len(fake.calls("create")) != 0 {
		t.Fatal("a sandbox was created")
	}
}

func TestMicroVMPutsWhatAPackageManagerInstallsOnAVolume(t *testing.T) {
	fake := newFakeMSB(t)
	project := t.TempDir()
	e := fake.executor()

	// No project files yet: nothing to create in the user's folder.
	if _, err := e.Run(context.Background(), RunRequest{Command: "x", WorkDir: project}); err != nil {
		t.Fatal(err)
	}
	name := microVMName(project)
	first := strings.Join(fake.calls("create")[0], " ")
	if strings.Contains(first, "node_modules") {
		t.Fatalf("a volume with no project to use it: %s", first)
	}

	// The agent scaffolds a project: msb cannot add a volume, so the sandbox is replaced.
	if err := os.WriteFile(filepath.Join(project, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), RunRequest{Command: "npm install", WorkDir: project}); err != nil {
		t.Fatal(err)
	}
	creates := fake.calls("create")
	if len(creates) != 2 || len(fake.calls("rm")) != 1 {
		t.Fatalf("creates %v rm %v", creates, fake.calls("rm"))
	}
	if !slices.Contains(creates[1], name+"-node_modules:/workspace/node_modules") {
		t.Fatalf("no volume: %v", creates[1])
	}

	// Nothing changed: not replaced again, and a volume that exists is not an error.
	if _, err := e.Run(context.Background(), RunRequest{Command: "x", WorkDir: project}); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls("create")) != 2 {
		t.Fatal("replaced with nothing to change")
	}
	if err := os.Remove(filepath.Join(fake.state, "sandbox-"+name)); err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), RunRequest{Command: "x", WorkDir: project}); err != nil {
		t.Fatalf("an existing volume must be reused: %v", err)
	}
}

func TestMicroVMASandboxIsSharedUntilItsLastUserCloses(t *testing.T) {
	fake := newFakeMSB(t)
	project := t.TempDir()
	first, second := fake.executor(), fake.executor()
	for _, e := range []*MicroVMExecutor{first, second} {
		if _, err := e.Run(context.Background(), RunRequest{Command: "x", WorkDir: project}); err != nil {
			t.Fatal(err)
		}
	}
	name := microVMName(project)

	task := first.BackgroundExecutor()
	if _, err := task.Run(context.Background(), RunRequest{Command: "serve", WorkDir: project}); err != nil {
		t.Fatal(err)
	}
	if closer, ok := task.(interface{ Close() error }); !ok || closer.Close() != nil {
		t.Fatal("a task must close without error")
	}
	if fake.status(name) != "Running" {
		t.Fatal("a background task ending stopped the sandbox")
	}

	if err := first.Close(); err != nil {
		t.Fatal(err)
	}
	if fake.status(name) != "Running" {
		t.Fatal("one client closing stopped the other's sandbox")
	}
	if err := second.Close(); err != nil {
		t.Fatal(err)
	}
	if fake.status(name) != "Stopped" {
		t.Fatalf("the last user closed, status %q", fake.status(name))
	}
	if err := second.Close(); err != nil {
		t.Fatalf("a second close: %v", err)
	}
	if _, err := first.Run(context.Background(), RunRequest{Command: "x", WorkDir: project}); err == nil {
		t.Fatal("a closed executor must refuse")
	}

	// The next session restarts it and creates nothing.
	third := fake.executor()
	if _, err := third.Run(context.Background(), RunRequest{Command: "x", WorkDir: project}); err != nil {
		t.Fatal(err)
	}
	if len(fake.calls("create")) != 1 || len(fake.calls("start")) != 1 {
		t.Fatalf("creates %v starts %v", fake.calls("create"), fake.calls("start"))
	}
}

func TestMicroVMHealthAndPrepare(t *testing.T) {
	fake := newFakeMSB(t)
	e := fake.executor()
	if err := e.Healthy(context.Background()); err != nil {
		t.Fatal(err)
	}
	if err := (microVMTask{e}).Healthy(context.Background()); err != nil {
		t.Fatal(err)
	}

	var progress strings.Builder
	if err := PrepareMicroVM(context.Background(), fake.cfg, func(line string) { progress.WriteString(line) }); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(progress.String(), DefaultMicroVMImage) {
		t.Fatalf("progress %q", progress.String())
	}

	if err := os.WriteFile(filepath.Join(fake.state, "no-hypervisor"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := e.Healthy(context.Background()); err == nil || !strings.Contains(err.Error(), "no hypervisor") {
		t.Fatalf("err %v", err)
	}
	if err := PrepareMicroVM(context.Background(), fake.cfg, nil); err == nil {
		t.Fatal("prepare must fail where a sandbox cannot run")
	}

	missing := MicroVMExecutor{cfg: MicroVMConfig{BinaryPath: filepath.Join(t.TempDir(), "no-msb"), Home: t.TempDir()}.withDefaults()}
	if err := missing.Healthy(context.Background()); err == nil {
		t.Fatal("a missing binary must not be healthy")
	}
	if _, err := NewMicroVMExecutor(MicroVMConfig{Home: "x"}); err == nil {
		t.Fatal("the binary is required")
	}
	if _, err := NewMicroVMExecutor(MicroVMConfig{BinaryPath: "x"}); err == nil {
		t.Fatal("the home is required")
	}
}

func TestMicroVMStdinIsRefused(t *testing.T) {
	fake := newFakeMSB(t)
	e := fake.executor()
	if _, err := e.Run(context.Background(), RunRequest{Command: "x", WorkDir: t.TempDir(), Stdin: strings.NewReader("y")}); err == nil {
		t.Fatal("stdin must be refused, not dropped")
	}
}

func TestMicroVMStopAndRemoveEverything(t *testing.T) {
	fake := newFakeMSB(t)
	project := t.TempDir()
	if err := os.WriteFile(filepath.Join(project, "package.json"), []byte("{}"), 0o644); err != nil {
		t.Fatal(err)
	}
	e, err := NewMicroVMExecutor(fake.cfg)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := e.Run(context.Background(), RunRequest{Command: "x", WorkDir: project}); err != nil {
		t.Fatal(err)
	}
	name := microVMName(project)
	// A crash: the executor never closes, and the sandbox is still running.
	if err := StopMicroVMEnvironments(context.Background(), fake.cfg); err != nil {
		t.Fatal(err)
	}
	if fake.status(name) != "Stopped" {
		t.Fatalf("status %q", fake.status(name))
	}
	if err := RemoveMicroVMEnvironments(context.Background(), fake.cfg); err != nil {
		t.Fatal(err)
	}
	if fake.status(name) != "" {
		t.Fatal("the sandbox is still there")
	}
	if _, err := os.Stat(filepath.Join(fake.state, "volume-"+name+"-node_modules")); err == nil {
		t.Fatal("the volume is still there")
	}
	if _, err := os.Stat(project); err != nil {
		t.Fatalf("the project must stay: %v", err)
	}
	// Nothing recorded is not an error.
	empty := MicroVMConfig{BinaryPath: fake.cfg.BinaryPath, Home: t.TempDir()}
	if err := StopMicroVMEnvironments(context.Background(), empty); err != nil {
		t.Fatal(err)
	}
	var record microVMRecord
	if err := json.Unmarshal([]byte(`{"root":"r","volumes":["a"]}`), &record); err != nil || record.Root != "r" {
		t.Fatal("record format")
	}
}

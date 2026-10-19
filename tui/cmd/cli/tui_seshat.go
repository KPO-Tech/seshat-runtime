package main

import (
	"context"
	"io"
	"log"
	"os"
	"path/filepath"
	"strings"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/KPO-Tech/seshat-runtime/internal/monitoring"
	engineconfig "github.com/KPO-Tech/seshat-runtime/pkg/config"
	"github.com/KPO-Tech/seshat-runtime/pkg/runtimepath"
	"github.com/KPO-Tech/seshat-runtime/pkg/sdk"
	"github.com/KPO-Tech/seshat-runtime/tui/cmd/cli/appdir"
	mcptools "github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/agent/tools/mcp"
	tuiconfig "github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/config"
	tuicommon "github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/common"
	uimodel "github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/model"
	tuiws "github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/workspace"
	uv "github.com/charmbracelet/ultraviolet"
)

// runSeshatTUI starts the Seshat TUI.
func runSeshatTUI(ctx context.Context, options runtimeOptions, initialSessionID string, continueLast bool) error {
	ensureSeshatTUIRuntimeRoot()
	// Create top-level dirs and seed seshat.json skeleton on first run.
	// Non-fatal: a missing logs/ or sessions/ dir is annoying but not fatal.
	_ = appdir.EnsureAppDirs()
	if err := validateProviderSetup(options); err != nil {
		return err
	}

	options.Monitoring = buildTUIMonitoring()
	if lf := openCLILogFile(); lf != nil {
		log.SetOutput(lf)
	} else {
		log.SetOutput(io.Discard)
	}

	modelStr := ""
	if options.Model.Provider != "" {
		modelStr = string(options.Model.Provider) + ":" + options.Model.Model
	}

	ws := tuiws.NewSeshatWorkspace(nil, options.WorkingDir, modelStr)
	ws.SetStartupConfig(options.SQLitePath, options.PermissionMode, options.Monitoring)
	if mcpStore, err := tuiconfig.LoadForMCP(options.WorkingDir); err == nil {
		ws.SetMCPConfig(mcpStore.Config().MCP)
		ws.SetMCPStore(mcpStore)
		applyCapabilityToolConfig(&options, mcpStore.Config())
		if o := mcpStore.Config().Options; o != nil && o.MemoryEnabled != nil {
			options.MemoryEnabled = *o.MemoryEnabled
		}
		if o := mcpStore.Config().Options; o != nil && o.RefreshModelCatalog != nil {
			options.RefreshCatalog = *o.RefreshModelCatalog
		}
		options.MCPServers = seshatMCPToSDK(mcpStore)
		go mcptools.Initialize(ctx, nil, mcpStore)
	} else if mcps := tuiconfig.LoadMCPConfig(options.WorkingDir); len(mcps) > 0 {
		ws.SetMCPConfig(mcps)
	}

	client, err := newClient(
		options,
		ws.PromptFn,
		ws.OnProgress,
		ws.OnChunk,
		ws.OnRuntimeEvent,
		ws.OnSessionTitled,
		ws.PlanStore(),
	)
	if err != nil {
		return err
	}
	ws.SetSDKClient(client)
	catchUpMemories(client, options.MemoryEnabled)

	// Probe env vars, credentials DB, and Ollama in the background so the
	// TUI starts immediately while provider detection completes concurrently.
	go ws.DetectProviders()

	com := tuicommon.DefaultCommon(ws)
	uiModel := uimodel.New(com, initialSessionID, continueLast)

	var env uv.Environ = os.Environ()
	p := tea.NewProgram(
		uiModel,
		tea.WithEnvironment(env),
		tea.WithContext(ctx),
		tea.WithFilter(uimodel.MouseEventFilter),
	)
	go ws.Subscribe(p)

	_, runErr := p.Run()

	ws.Shutdown()
	return runErr
}

func buildTUIMonitoring() *sdk.MonitoringSystem {
	logDir := runtimepath.LogsDir("")
	_ = os.MkdirAll(logDir, 0o755)
	logPath := filepath.Join(logDir, "app.log")
	logger := monitoring.NewLoggerWithConfig(&monitoring.LoggerConfig{
		Level:    monitoring.LogLevelInfo,
		Output:   "file",
		FilePath: logPath,
		Format:   "text",
	})
	return monitoring.NewSystem(logger)
}

func defaultSeshatTUIRuntimeRoot() string {
	return runtimepath.DefaultConfigDir("seshat-tui")
}

func ensureSeshatTUIRuntimeRoot() {
	if strings.TrimSpace(os.Getenv(runtimepath.EnvRuntimeRoot)) != "" {
		return
	}
	_ = os.Setenv(runtimepath.EnvRuntimeRoot, defaultSeshatTUIRuntimeRoot())
}

func openCLILogFile() *os.File {
	config, err := engineconfig.Load()
	if err != nil {
		return nil
	}
	logDir := filepath.Join(config.RuntimeRoot, "logs")
	if err := os.MkdirAll(logDir, 0o755); err != nil {
		return nil
	}
	f, err := os.OpenFile(filepath.Join(logDir, "cli.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o640)
	if err != nil {
		return nil
	}
	return f
}

// seshatMCPToSDK converts seshat.json MCP entries into the engine-side
// MCPServerConfig slice so the agent can discover and call MCP tools.
func seshatMCPToSDK(store *tuiconfig.ConfigStore) []sdk.MCPServerConfig {
	r := store.Resolver()
	cfg := store.Config()
	out := make([]sdk.MCPServerConfig, 0, len(cfg.MCP))
	for name, m := range cfg.MCP {
		if m.Disabled {
			continue
		}
		sc := sdk.MCPServerConfig{Name: name}
		switch m.Type {
		case tuiconfig.MCPStdio:
			sc.Transport = sdk.MCPTransportStdio
			sc.Command = m.Command
			sc.Args, _ = m.ResolvedArgs(r)
			if envSlice, err := m.ResolvedEnv(r); err == nil {
				sc.Env = envSliceToMap(envSlice)
			}
		case tuiconfig.MCPHttp:
			sc.Transport = sdk.MCPTransportHTTP
			sc.URL, _ = m.ResolvedURL(r)
			sc.Headers, _ = m.ResolvedHeaders(r)
		case tuiconfig.MCPSSE:
			sc.Transport = sdk.MCPTransportSSE
			sc.URL, _ = m.ResolvedURL(r)
			sc.Headers, _ = m.ResolvedHeaders(r)
		default:
			continue
		}
		if m.Timeout > 0 {
			sc.Timeout = time.Duration(m.Timeout) * time.Second
		}
		out = append(out, sc)
	}
	return out
}

func envSliceToMap(envs []string) map[string]string {
	if len(envs) == 0 {
		return nil
	}
	m := make(map[string]string, len(envs))
	for _, entry := range envs {
		k, v, _ := strings.Cut(entry, "=")
		if k != "" {
			m[k] = v
		}
	}
	return m
}

func applyCapabilityToolConfig(options *runtimeOptions, cfg *tuiconfig.Config) {
	if options == nil || cfg == nil {
		return
	}
	options.ImageGeneration = buildSDKImageGenerationConfig(cfg)
	options.TextToSpeech = buildSDKTextToSpeechConfig(cfg)
	options.SpeechToText = buildSDKSpeechToTextConfig(cfg)
}

func buildSDKImageGenerationConfig(cfg *tuiconfig.Config) *sdk.ImageGenerationConfig {
	if cfg == nil || cfg.ImageGeneration == nil {
		return nil
	}
	providerID := strings.TrimSpace(cfg.ImageGeneration.Provider)
	if providerID == "" {
		return nil
	}
	out := &sdk.ImageGenerationConfig{Provider: providerID, Model: strings.TrimSpace(cfg.ImageGeneration.Model)}
	if pc, ok := cfg.Providers.Get(providerID); ok {
		out.APIKey = strings.TrimSpace(pc.APIKey)
		out.BaseURL = strings.TrimSpace(pc.BaseURL)
	}
	return out
}

func buildSDKTextToSpeechConfig(cfg *tuiconfig.Config) *sdk.TextToSpeechConfig {
	if cfg == nil || cfg.TextToSpeech == nil {
		return nil
	}
	providerID := strings.TrimSpace(cfg.TextToSpeech.Provider)
	if providerID == "" {
		return nil
	}
	out := &sdk.TextToSpeechConfig{
		Provider: providerID,
		Model:    strings.TrimSpace(cfg.TextToSpeech.Model),
		Voice:    strings.TrimSpace(cfg.TextToSpeech.Voice),
		Format:   strings.TrimSpace(cfg.TextToSpeech.Format),
	}
	if pc, ok := cfg.Providers.Get(providerID); ok {
		out.APIKey = strings.TrimSpace(pc.APIKey)
		out.BaseURL = strings.TrimSpace(pc.BaseURL)
	}
	return out
}

func buildSDKSpeechToTextConfig(cfg *tuiconfig.Config) *sdk.SpeechToTextConfig {
	if cfg == nil || cfg.SpeechToText == nil {
		return nil
	}
	providerID := strings.TrimSpace(cfg.SpeechToText.Provider)
	if providerID == "" {
		return nil
	}
	out := &sdk.SpeechToTextConfig{
		Provider: providerID,
		Model:    strings.TrimSpace(cfg.SpeechToText.Model),
		Language: strings.TrimSpace(cfg.SpeechToText.Language),
	}
	if pc, ok := cfg.Providers.Get(providerID); ok {
		out.APIKey = strings.TrimSpace(pc.APIKey)
		out.BaseURL = strings.TrimSpace(pc.BaseURL)
	}
	return out
}

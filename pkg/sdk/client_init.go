package sdk

import (
	"context"
	"fmt"
	"log"
	"os"
	"strings"

	audioproviders "github.com/KPO-Tech/seshat-runtime/internal/audio/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/audio/stt"
	"github.com/KPO-Tech/seshat-runtime/internal/audio/tts"
	"github.com/KPO-Tech/seshat-runtime/internal/image"
	imageproviders "github.com/KPO-Tech/seshat-runtime/internal/image/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/memory"
	"github.com/KPO-Tech/seshat-runtime/internal/monitoring"
	"github.com/KPO-Tech/seshat-runtime/internal/permissions"
	"github.com/KPO-Tech/seshat-runtime/internal/storage"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/builtin"
	"github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
	browsercore "github.com/KPO-Tech/seshat-runtime/internal/web/browser"
)

func initArtifactStore(config *ClientConfig) ArtifactStore {
	switch {
	case config.ArtifactStore != nil:
		return config.ArtifactStore
	case config.StorageConfig != nil:
		store, err := NewArtifactStoreFromConfig(*config.StorageConfig)
		if err != nil {
			log.Printf("[sdk] storage artifact store unavailable from config, continuing without persisted web artifacts: %v", err)
			return nil
		}
		return store
	default:
		store, err := DefaultArtifactStore()
		if err != nil {
			log.Printf("[sdk] storage artifact store unavailable, continuing without persisted web artifacts: %v", err)
			return nil
		}
		return store
	}
}

func initBrowserManager(config *ClientConfig, artifactStore ArtifactStore) (browsercore.Manager, *storage.Reaper) {
	browserConfig := browsercore.DefaultConfig()
	browserConfig.ArtifactStore = artifactStore
	browserConfig.RemoteControlURL = strings.TrimSpace(config.BrowserRemoteControlURL)
	browserConfig.ExecutablePath = strings.TrimSpace(config.BrowserExecutablePath)
	if resolve := config.BrowserSessionTargetResolver; resolve != nil {
		// SessionID (this package) is a true alias of the browser package's
		// types.SessionID (see types.go), so this closure's signature is
		// identical to browsercore.Config.TargetResolver's - no conversion,
		// no need to import internal/types here.
		browserConfig.TargetResolver = func(ctx context.Context, sessionID SessionID) (string, bool) {
			return resolve(ctx, sessionID)
		}
	}
	browserManager := browsercore.NewManager(browserConfig)

	var reaper *storage.Reaper
	if artifactStore != nil && config.StorageGCEnabled {
		reaper = storage.NewReaper(artifactStore, storage.ReaperConfig{
			Interval:   config.StorageGCInterval,
			Namespaces: parseStorageNamespaces(config.StorageGCNamespaces),
			Limit:      config.StorageGCLimit,
		})
		reaper.Start(context.Background())
	}
	return browserManager, reaper
}

func initSessionStore(config *ClientConfig) (SessionStore, bool, error) {
	switch {
	case config.SessionStore != nil:
		return config.SessionStore, false, nil
	case config.SessionBackend != nil:
		store, err := NewSessionStoreWithBackend(config.SessionBackend)
		if err != nil {
			return nil, false, err
		}
		return store, false, nil
	case config.SessionSQLitePath != "":
		backend, err := OpenSQLiteSessionBackend(config.SessionSQLitePath)
		if err != nil {
			return nil, false, err
		}
		store, err := NewSessionStoreWithBackend(backend)
		if err != nil {
			return nil, false, err
		}
		return store, true, nil
	case config.PersistSessions:
		store, err := NewSessionStore(config.SessionStorageDir)
		if err != nil {
			return nil, false, err
		}
		return store, true, nil
	default:
		return nil, false, nil
	}
}

func initMemoryService(config *ClientConfig) (*memory.Service, error) {
	if !config.EnableMemory {
		return nil, nil
	}
	svc, err := memory.NewService()
	if err != nil {
		if config.MemoryFailFast {
			return nil, err
		}
		log.Printf("[sdk] memory service unavailable, continuing without memory: %v", err)
		return nil, err
	}
	return svc, nil
}

func initMonitoringSystem(config *ClientConfig) *monitoring.System {
	if !config.EnableMonitoring {
		return nil
	}
	if config.Monitoring != nil {
		return config.Monitoring
	}
	return monitoring.NewSystem(nil)
}

// newPermissionEngine builds the permission engine of a client: the default rules, then the deny
// rules of the organization's managed policy, which outrank every other rule.
func newPermissionEngine(config *ClientConfig) (*permissions.Engine, error) {
	engine := permissions.NewEngine()
	if err := engine.AddRules(permissions.NewDefaultRules()); err != nil {
		return nil, fmt.Errorf("add default permission rules: %w", err)
	}
	if err := engine.AddRules(managedDenyRules(config.ManagedPolicy)); err != nil {
		return nil, fmt.Errorf("add managed deny rules: %w", err)
	}
	return engine, nil
}

func initBuiltinRegistry(config *ClientConfig, browserManager browsercore.Manager, artifactStore ArtifactStore) (*registry.Registry, error) {
	reg, err := builtin.NewBuiltinRegistryWithConfig(&builtin.Config{
		WorkingDir:                 config.WorkingDir,
		PromptFn:                   config.PromptFn,
		EnablePromptReaderFallback: config.EnablePromptReaderFallback,
		BrowserManager:             browserManager,
		ArtifactStore:              artifactStore,
		RAGService:                 config.RAGService,
		PlanStore:                  config.PlanStore,
		UserID:                     config.UserID,
		LongTermMemory:             config.LongTermMemory,
		DocumentReaderURL:          config.DocumentReaderURL,
		DocumentConverter:          config.DocumentConverter,
		DocumentPageRenderer:       config.DocumentPageRenderer,
		AutomationServiceURL:       config.AutomationServiceURL,
		AutomationAPIKey:           config.AutomationAPIKey,
		WebSearchKeys:              config.WebSearchKeys,
		ResearchKeys:               config.ResearchKeys,
		ImageGenerator:             initImageGenerator(config),
		TTSGenerator:               initTextToSpeechGenerator(config),
		STTTranscriber:             initSpeechToTextTranscriber(config),
		RequireSandbox:             config.RequireSandbox,
		SandboxKind:                config.SandboxKind,
		SandboxDocker:              config.SandboxDocker,
		EnableToolSearch:           config.EnableToolSearch,
	})
	if err != nil {
		return nil, err
	}
	// The model should not even see a tool the organization forbids; its deny rule covers the rest.
	hideForbiddenTools(config.ManagedPolicy, reg)
	return reg, nil
}

func initImageGenerator(config *ClientConfig) image.Generation {
	cfg := config.ImageGeneration
	if cfg == nil || strings.TrimSpace(cfg.Provider) == "" {
		return nil
	}
	providerID := strings.ToLower(strings.TrimSpace(cfg.Provider))
	profile, known := imageproviders.ProfileFor(providerID)
	apiKey := resolveCapabilityAPIKey(config, providerID, strings.TrimSpace(cfg.APIKey))
	if apiKey == "" {
		for _, name := range profile.EnvVars {
			if value := strings.TrimSpace(os.Getenv(name)); value != "" {
				apiKey = value
				break
			}
		}
	}
	baseURL := resolveCapabilityBaseURL(config, providerID, strings.TrimSpace(cfg.BaseURL))
	custom := baseURL != ""
	// A provider that is not in the catalog is taken for an OpenAI-compatible
	// image server, which needs the host to say where it is.
	api := profile.API
	if !known {
		if !custom {
			logCapabilityUnknownProvider("image generation", "generate_image", providerID)
			return nil
		}
		api = imageproviders.APIOpenAI
	}
	// A custom endpoint is usually a self-hosted server that does not check the
	// Authorization header, so requiring a cloud key there would block a valid
	// local-only setup. Only a catalog endpoint needs a key.
	if apiKey == "" && !custom {
		logCapabilityCredentialsMissing("image generation", "generate_image", providerID)
		return nil
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = profile.DefaultModel()
	}

	switch api {
	case imageproviders.APIGemini:
		opts := []imageproviders.GeminiOption{}
		if model != "" {
			opts = append(opts, imageproviders.WithGeminiModel(model))
		}
		if custom {
			opts = append(opts, imageproviders.WithGeminiBaseURL(ensureGeminiBaseURL(baseURL)))
		}
		if ratio := strings.TrimSpace(cfg.AspectRatio); ratio != "" {
			opts = append(opts, imageproviders.WithGeminiAspectRatio(ratio))
		}
		return imageproviders.NewGemini(apiKey, opts...)
	case imageproviders.APIOpenRouter:
		opts := []imageproviders.OpenRouterOption{}
		if model != "" {
			opts = append(opts, imageproviders.WithOpenRouterModel(model))
		}
		if custom {
			opts = append(opts, imageproviders.WithOpenRouterBaseURL(ensureOpenAIBaseURL(baseURL)))
		}
		if ratio := strings.TrimSpace(cfg.AspectRatio); ratio != "" {
			opts = append(opts, imageproviders.WithOpenRouterAspectRatio(ratio))
		}
		return imageproviders.NewOpenRouter(apiKey, opts...)
	default:
		opts := []imageproviders.OpenAIOption{imageproviders.WithOpenAIProviderName(providerID)}
		if model != "" {
			opts = append(opts, imageproviders.WithOpenAIModel(model))
		}
		if custom {
			opts = append(opts, imageproviders.WithOpenAIBaseURL(ensureOpenAIBaseURL(baseURL)))
		}
		if size := strings.TrimSpace(cfg.Size); size != "" {
			opts = append(opts, imageproviders.WithOpenAISize(size))
		}
		if quality := strings.TrimSpace(cfg.Quality); quality != "" {
			opts = append(opts, imageproviders.WithOpenAIQuality(quality))
		}
		return imageproviders.NewOpenAI(apiKey, opts...)
	}
}

// logCapabilityCredentialsMissing warns when a host explicitly selected a
// provider for a secondary capability (image generation, text-to-speech,
// speech-to-text) but no API key or base URL could be resolved for it, so the
// tool silently stays disabled instead of erroring on every call. Capability
// config left unset entirely (the common case for hosts not using the
// feature) does not reach this path and stays silent.
func logCapabilityCredentialsMissing(capability, toolName, providerID string) {
	log.Printf("[sdk] %s configured with provider %q but no API key or base URL was resolved for it; %s will stay disabled until credentials are set", capability, providerID, toolName)
}

// logCapabilityUnknownProvider warns when a host selected a provider ID for a
// secondary capability that this build does not implement.
func logCapabilityUnknownProvider(capability, toolName, providerID string) {
	log.Printf("[sdk] %s configured with unrecognized provider %q; %s will stay disabled", capability, providerID, toolName)
}

// audioEndpoint is what the text-to-speech and speech-to-text tools need to
// reach a provider: where to send the request and with which key.
type audioEndpoint struct {
	profile audioproviders.Profile
	known   bool
	apiKey  string
	// baseURL is the OpenAI-compatible API root, already completed with /v1
	// when the host gave a custom one.
	baseURL string
	// custom is true when the host named the endpoint itself.
	custom bool
}

// resolveAudioEndpoint looks a provider up in the audio catalog
// (internal/audio/providers) instead of accepting only "openai": a listed
// provider gets its default endpoint and environment key, and any other ID works
// when the host gives its base URL (a self-hosted OpenAI-compatible server).
// ok is false, after a log line, when the tool must stay disabled.
func resolveAudioEndpoint(config *ClientConfig, capability, toolName, providerID, explicitKey, explicitURL string, direction func(audioproviders.Profile) *audioproviders.Capability) (audioEndpoint, bool) {
	profile, known := audioproviders.ProfileFor(providerID)
	if known && direction(profile) == nil {
		log.Printf("[sdk] %s configured with provider %q, which does not offer it; %s will stay disabled", capability, providerID, toolName)
		return audioEndpoint{}, false
	}
	ep := audioEndpoint{profile: profile, known: known}
	ep.apiKey = resolveCapabilityAPIKey(config, providerID, explicitKey)
	if ep.apiKey == "" {
		for _, name := range profile.EnvVars {
			if value := strings.TrimSpace(os.Getenv(name)); value != "" {
				ep.apiKey = value
				break
			}
		}
	}
	if baseURL := resolveCapabilityBaseURL(config, providerID, explicitURL); baseURL != "" {
		if profile.API == "" {
			baseURL = ensureOpenAIBaseURL(baseURL)
		}
		ep.baseURL = baseURL
		ep.custom = true
	} else if known {
		ep.baseURL = profile.BaseURL
	} else {
		logCapabilityUnknownProvider(capability, toolName, providerID)
		return audioEndpoint{}, false
	}
	// A custom endpoint is usually a self-hosted server that does not check the
	// Authorization header, so requiring a cloud key there would block a valid
	// local-only setup. Only a catalog endpoint needs a key.
	if ep.apiKey == "" && !ep.custom {
		logCapabilityCredentialsMissing(capability, toolName, providerID)
		return audioEndpoint{}, false
	}
	return ep, true
}

func newElevenLabsTTS(ep audioEndpoint, cfg *TextToSpeechConfig) tts.Generation {
	opts := []audioproviders.ElevenLabsTTSOption{audioproviders.WithElevenLabsTTSBaseURL(ep.baseURL)}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = ep.profile.TTS.DefaultModel()
	}
	opts = append(opts, audioproviders.WithElevenLabsTTSModel(model))
	if voice := strings.TrimSpace(cfg.Voice); voice != "" {
		opts = append(opts, audioproviders.WithElevenLabsTTSVoice(voice))
	}
	if format := strings.TrimSpace(cfg.Format); format != "" {
		opts = append(opts, audioproviders.WithElevenLabsTTSFormat(format))
	}
	return audioproviders.NewElevenLabsTTS(ep.apiKey, opts...)
}

func newElevenLabsSTT(ep audioEndpoint, cfg *SpeechToTextConfig) stt.SpeechToText {
	opts := []audioproviders.ElevenLabsSTTOption{audioproviders.WithElevenLabsSTTBaseURL(ep.baseURL)}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = ep.profile.STT.DefaultModel()
	}
	opts = append(opts, audioproviders.WithElevenLabsSTTModel(model))
	if language := strings.TrimSpace(cfg.Language); language != "" {
		opts = append(opts, audioproviders.WithElevenLabsSTTLanguage(language))
	}
	return audioproviders.NewElevenLabsSTT(ep.apiKey, opts...)
}

func initTextToSpeechGenerator(config *ClientConfig) tts.Generation {
	cfg := config.TextToSpeech
	if cfg == nil || strings.TrimSpace(cfg.Provider) == "" {
		return nil
	}
	providerID := strings.ToLower(strings.TrimSpace(cfg.Provider))
	ep, ok := resolveAudioEndpoint(config, "text-to-speech", "text_to_speech", providerID, strings.TrimSpace(cfg.APIKey), strings.TrimSpace(cfg.BaseURL),
		func(p audioproviders.Profile) *audioproviders.Capability { return p.TTS })
	if !ok {
		return nil
	}
	if ep.profile.API == audioproviders.APIElevenLabs {
		return newElevenLabsTTS(ep, cfg)
	}
	opts := []audioproviders.OpenAITTSOption{
		audioproviders.WithTTSProviderName(providerID),
		audioproviders.WithTTSBaseURL(ep.baseURL),
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = ep.profile.TTS.DefaultModel()
	}
	if model != "" {
		opts = append(opts, audioproviders.WithTTSModel(model))
	}
	if voice := strings.TrimSpace(cfg.Voice); voice != "" {
		opts = append(opts, audioproviders.WithTTSVoice(voice))
	}
	if format := strings.TrimSpace(cfg.Format); format != "" {
		opts = append(opts, audioproviders.WithTTSFormat(format))
	}
	return audioproviders.NewOpenAITTS(ep.apiKey, opts...)
}

func initSpeechToTextTranscriber(config *ClientConfig) stt.SpeechToText {
	cfg := config.SpeechToText
	if cfg == nil || strings.TrimSpace(cfg.Provider) == "" {
		return nil
	}
	providerID := strings.ToLower(strings.TrimSpace(cfg.Provider))
	ep, ok := resolveAudioEndpoint(config, "speech-to-text", "speech_to_text", providerID, strings.TrimSpace(cfg.APIKey), strings.TrimSpace(cfg.BaseURL),
		func(p audioproviders.Profile) *audioproviders.Capability { return p.STT })
	if !ok {
		return nil
	}
	if ep.profile.API == audioproviders.APIElevenLabs {
		return newElevenLabsSTT(ep, cfg)
	}
	opts := []audioproviders.OpenAISTTOption{
		audioproviders.WithSTTProviderName(providerID),
		audioproviders.WithSTTBaseURL(ep.baseURL),
	}
	if ep.known {
		opts = append(opts, audioproviders.WithSTTResponseFormat(ep.profile.STTResponseFormat))
	}
	model := strings.TrimSpace(cfg.Model)
	if model == "" {
		model = ep.profile.STT.DefaultModel()
	}
	if model != "" {
		opts = append(opts, audioproviders.WithSTTModel(model))
	}
	if language := strings.TrimSpace(cfg.Language); language != "" {
		opts = append(opts, audioproviders.WithSTTLanguage(language))
	}
	return audioproviders.NewOpenAISTT(ep.apiKey, opts...)
}

func resolveCapabilityAPIKey(config *ClientConfig, providerID, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if strings.EqualFold(providerID, string(config.Model.Provider)) {
		if config.ProviderConfig != nil && strings.TrimSpace(config.ProviderConfig.APIKey) != "" {
			return strings.TrimSpace(config.ProviderConfig.APIKey)
		}
		if strings.TrimSpace(config.APIKey) != "" {
			return strings.TrimSpace(config.APIKey)
		}
	}
	if config.CredentialResolver != nil {
		if key, err := config.CredentialResolver.ResolveAPIKey(context.Background(), providerID); err == nil {
			return strings.TrimSpace(key)
		}
	}
	return ""
}

func resolveCapabilityBaseURL(config *ClientConfig, providerID, explicit string) string {
	if explicit != "" {
		return explicit
	}
	if strings.EqualFold(providerID, string(config.Model.Provider)) && config.ProviderConfig != nil {
		return strings.TrimSpace(config.ProviderConfig.BaseURL)
	}
	return ""
}

func ensureOpenAIBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	if strings.HasSuffix(baseURL, "/v1") {
		return baseURL
	}
	return baseURL + "/v1"
}

func ensureGeminiBaseURL(baseURL string) string {
	baseURL = strings.TrimRight(strings.TrimSpace(baseURL), "/")
	if baseURL == "" {
		return ""
	}
	if strings.Contains(baseURL, "/v1beta") {
		return baseURL
	}
	return baseURL + "/v1beta"
}

func parseStorageNamespaces(raw []string) []storage.ArtifactNamespace {
	namespaces := make([]storage.ArtifactNamespace, 0, len(raw))
	for _, item := range raw {
		value := strings.TrimSpace(item)
		if value == "" {
			continue
		}
		namespaces = append(namespaces, storage.ArtifactNamespace(value))
	}
	return namespaces
}

package dialog

import (
	"fmt"
	"strings"
	"time"

	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/config"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/common"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/styles"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/util"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/x/exp/charmtone"
)

type APIKeyInputState int

const (
	APIKeyInputStateInitial APIKeyInputState = iota
	APIKeyInputStateVerifying
	APIKeyInputStateVerified
	APIKeyInputStateError
)

// APIKeyInputID is the identifier for the model selection dialog.
const APIKeyInputID = "api_key_input"

// APIKeyInput represents a provider configuration dialog.
type APIKeyInput struct {
	com          *common.Common
	isOnboarding bool

	provider  catwalk.Provider
	model     config.SelectedModel
	modelType config.SelectedModelType

	width int
	state APIKeyInputState
	err   error

	hasAPIKey      bool
	existingAPIKey string
	defaultBaseURL string
	focused        int

	keyMap struct {
		Submit   key.Binding
		Next     key.Binding
		Previous key.Binding
		Reveal   key.Binding
		Close    key.Binding
	}
	apiKeyInput  textinput.Model
	baseURLInput textinput.Model
	spinner      spinner.Model
	help         help.Model
}

var _ Dialog = (*APIKeyInput)(nil)

// NewAPIKeyInput creates a new provider configuration dialog.
func NewAPIKeyInput(
	com *common.Common,
	isOnboarding bool,
	provider catwalk.Provider,
	model config.SelectedModel,
	modelType config.SelectedModelType,
) (*APIKeyInput, tea.Cmd) {
	t := com.Styles

	m := APIKeyInput{}
	m.com = com
	m.isOnboarding = isOnboarding
	m.provider = provider
	m.model = model
	m.modelType = modelType
	m.width = 72
	m.hasAPIKey = providerNeedsAPIKey(string(provider.ID))

	cfg := com.Config()
	if cfg != nil {
		if providerCfg, ok := cfg.Providers.Get(string(provider.ID)); ok {
			m.existingAPIKey = strings.TrimSpace(providerCfg.APIKey)
			if v := strings.TrimSpace(providerCfg.BaseURL); v != "" {
				m.defaultBaseURL = v
			}
		}
	}
	if m.defaultBaseURL == "" {
		m.defaultBaseURL = strings.TrimSpace(provider.APIEndpoint)
	}
	if m.defaultBaseURL == "" && strings.EqualFold(string(provider.ID), "ollama") {
		m.defaultBaseURL = "http://localhost:11434"
	}

	innerWidth := m.width - t.Dialog.View.GetHorizontalFrameSize() - 2
	inputWidth := textInputWidth(t.Dialog.InputPrompt, innerWidth)

	m.apiKeyInput = newSecretInput()
	m.apiKeyInput.Placeholder = "Enter your API key..."
	if m.existingAPIKey != "" {
		m.apiKeyInput.Placeholder = "Leave blank to keep your saved API key"
	}
	m.apiKeyInput.SetStyles(com.Styles.TextInput)
	m.apiKeyInput.SetWidth(inputWidth)

	m.baseURLInput = textinput.New()
	m.baseURLInput.SetVirtualCursor(false)
	m.baseURLInput.Placeholder = "Provider base URL"
	m.baseURLInput.SetStyles(com.Styles.TextInput)
	m.baseURLInput.SetWidth(inputWidth)
	m.baseURLInput.SetValue(m.defaultBaseURL)

	m.focusField(0)

	m.spinner = spinner.New(
		spinner.WithSpinner(spinner.Dot),
		spinner.WithStyle(t.Dialog.APIKey.Spinner),
	)

	m.help = help.New()
	m.help.Styles = t.DialogHelpStyles()

	m.keyMap.Submit = key.NewBinding(
		key.WithKeys("enter", "ctrl+y"),
		key.WithHelp("enter", "submit"),
	)
	m.keyMap.Next = key.NewBinding(
		key.WithKeys("tab", "down"),
		key.WithHelp("tab", "next"),
	)
	m.keyMap.Previous = key.NewBinding(
		key.WithKeys("shift+tab", "up"),
		key.WithHelp("shift+tab", "prev"),
	)
	m.keyMap.Reveal = key.NewBinding(
		key.WithKeys("ctrl+r"),
		key.WithHelp("ctrl+r", "reveal key"),
	)
	m.keyMap.Close = CloseKey

	return &m, nil
}

func providerNeedsAPIKey(providerID string) bool {
	switch strings.ToLower(strings.TrimSpace(providerID)) {
	case "ollama":
		return false
	default:
		return true
	}
}

// apiKeyVisible reports whether the API key field currently shows its value.
func (m *APIKeyInput) apiKeyVisible() bool {
	return m.apiKeyInput.EchoMode != textinput.EchoPassword
}

// setAPIKeyVisible shows or hides the API key value. The value is hidden by
// default, and hidden again as soon as the field loses focus or the
// configuration is validated, so a key is only ever on screen when the user
// explicitly asks for it.
func (m *APIKeyInput) setAPIKeyVisible(visible bool) {
	if visible {
		m.apiKeyInput.EchoMode = textinput.EchoNormal
		return
	}
	m.apiKeyInput.EchoMode = textinput.EchoPassword
}

func (m *APIKeyInput) focusField(index int) {
	if m.hasAPIKey {
		m.apiKeyInput.Blur()
	}
	m.baseURLInput.Blur()

	count := m.fieldCount()
	if count <= 0 {
		m.focused = 0
		return
	}
	m.focused = ((index % count) + count) % count
	if m.hasAPIKey && m.focused == 0 {
		m.apiKeyInput.Focus()
		return
	}
	m.baseURLInput.Focus()
}

func (m *APIKeyInput) fieldCount() int {
	if m.hasAPIKey {
		return 2
	}
	return 1
}

func (m *APIKeyInput) focusedInput() *textinput.Model {
	if m.hasAPIKey && m.focused == 0 {
		return &m.apiKeyInput
	}
	return &m.baseURLInput
}

func (m *APIKeyInput) apiKeyValue() string {
	value := strings.TrimSpace(m.apiKeyInput.Value())
	if value == "" {
		value = strings.TrimSpace(m.existingAPIKey)
	}
	return value
}

func (m *APIKeyInput) baseURLValue() string {
	value := strings.TrimSpace(m.baseURLInput.Value())
	if value == "" {
		value = strings.TrimSpace(m.defaultBaseURL)
	}
	return value
}

// ID implements Dialog.
func (m *APIKeyInput) ID() string {
	return APIKeyInputID
}

// HandleMsg implements [Dialog].
func (m *APIKeyInput) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case ActionChangeAPIKeyState:
		m.state = msg.State
		m.err = msg.Error
		switch m.state {
		case APIKeyInputStateInitial:
			m.focusField(m.focused)
		case APIKeyInputStateVerifying:
			m.setAPIKeyVisible(false)
			cmd := tea.Batch(m.spinner.Tick, m.verifyProviderConfig)
			return ActionCmd{cmd}
		case APIKeyInputStateVerified:
			if m.hasAPIKey {
				m.setAPIKeyVisible(false)
				m.apiKeyInput.Blur()
			}
			m.baseURLInput.Blur()
		case APIKeyInputStateError:
			m.focusField(m.focused)
		}
	case spinner.TickMsg:
		if m.state == APIKeyInputStateVerifying {
			var cmd tea.Cmd
			m.spinner, cmd = m.spinner.Update(msg)
			if cmd != nil {
				return ActionCmd{cmd}
			}
		}
	case tea.KeyPressMsg:
		switch {
		case m.state == APIKeyInputStateVerifying:
			return nil
		case key.Matches(msg, m.keyMap.Close):
			switch m.state {
			case APIKeyInputStateVerified:
				return m.saveKeyAndContinue()
			default:
				return ActionClose{}
			}
		case key.Matches(msg, m.keyMap.Next) && m.fieldCount() > 1:
			m.state = APIKeyInputStateInitial
			m.setAPIKeyVisible(false)
			m.focusField(m.focused + 1)
		case key.Matches(msg, m.keyMap.Previous) && m.fieldCount() > 1:
			m.state = APIKeyInputStateInitial
			m.setAPIKeyVisible(false)
			m.focusField(m.focused - 1)
		case key.Matches(msg, m.keyMap.Reveal) && m.hasAPIKey && m.focused == 0 &&
			m.state != APIKeyInputStateVerifying && m.state != APIKeyInputStateVerified:
			m.setAPIKeyVisible(!m.apiKeyVisible())
		case key.Matches(msg, m.keyMap.Submit):
			switch m.state {
			case APIKeyInputStateInitial, APIKeyInputStateError:
				return ActionChangeAPIKeyState{State: APIKeyInputStateVerifying}
			case APIKeyInputStateVerified:
				return m.saveKeyAndContinue()
			}
		default:
			m.state = APIKeyInputStateInitial
			m.err = nil
			var cmd tea.Cmd
			focused := m.focusedInput()
			*focused, cmd = focused.Update(msg)
			if cmd != nil {
				return ActionCmd{cmd}
			}
		}
	case tea.PasteMsg:
		m.state = APIKeyInputStateInitial
		m.err = nil
		var cmd tea.Cmd
		focused := m.focusedInput()
		*focused, cmd = focused.Update(msg)
		if cmd != nil {
			return ActionCmd{cmd}
		}
	}
	return nil
}

// Draw implements [Dialog].
func (m *APIKeyInput) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := m.com.Styles

	textStyle := t.Dialog.SecondaryText
	helpStyle := t.Dialog.HelpView
	dialogStyle := t.Dialog.View.Width(m.width)
	inputStyle := t.Dialog.InputPrompt
	helpStyle = helpStyle.Width(m.width - dialogStyle.GetHorizontalFrameSize())

	contentParts := []string{m.headerView()}
	if m.hasAPIKey {
		contentParts = append(contentParts,
			textStyle.Render("  API key"),
			inputStyle.Render(m.inputView(&m.apiKeyInput, m.hasAPIKey && m.focused == 0)),
		)
		if m.existingAPIKey != "" {
			contentParts = append(contentParts, textStyle.Render("  Leave the API key blank to keep the saved credential."))
		}
		contentParts = append(contentParts, "")
	}
	contentParts = append(contentParts,
		textStyle.Render("  Base URL"),
		inputStyle.Render(m.inputView(&m.baseURLInput, !m.hasAPIKey || m.focused == 1)),
	)
	if m.defaultBaseURL != "" {
		contentParts = append(contentParts, textStyle.Render("  Default endpoint: "+m.defaultBaseURL))
	}
	if m.state == APIKeyInputStateError && m.err != nil {
		contentParts = append(contentParts, t.Dialog.TitleError.Render(m.err.Error()))
	}
	contentParts = append(contentParts,
		"",
		textStyle.Render("  Configuration is saved for future sessions."),
		"",
		helpStyle.Render(m.help.View(m)),
	)
	content := strings.Join(contentParts, "\n")

	cur := m.Cursor()
	if m.isOnboarding {
		view := content
		cur = adjustOnboardingInputCursor(t, cur)
		DrawOnboardingCursor(scr, area, view, cur)
	} else {
		view := dialogStyle.Render(content)
		DrawCenterCursor(scr, area, view, cur)
	}
	return cur
}

func (m *APIKeyInput) headerView() string {
	var (
		t           = m.com.Styles
		titleStyle  = t.Dialog.Title
		textStyle   = t.Dialog.PrimaryText
		dialogStyle = t.Dialog.View.Width(m.width)
	)
	if m.isOnboarding {
		return textStyle.Render(m.dialogTitle())
	}
	headerOffset := titleStyle.GetHorizontalFrameSize() + dialogStyle.GetHorizontalFrameSize()
	return common.DialogTitle(t, titleStyle.Render(m.dialogTitle()), m.width-headerOffset, m.com.Styles.Dialog.TitleGradFromColor, m.com.Styles.Dialog.TitleGradToColor)
}

func (m *APIKeyInput) dialogTitle() string {
	var (
		t           = m.com.Styles
		textStyle   = t.Dialog.TitleText
		errorStyle  = t.Dialog.TitleError
		accentStyle = t.Dialog.TitleAccent
	)
	switch m.state {
	case APIKeyInputStateInitial:
		return textStyle.Render("Configure ") + accentStyle.Render(string(m.provider.Name)) + textStyle.Render(".")
	case APIKeyInputStateVerifying:
		return textStyle.Render("Validating ") + accentStyle.Render(string(m.provider.Name)) + textStyle.Render(" configuration...")
	case APIKeyInputStateVerified:
		return accentStyle.Render(string(m.provider.Name)) + textStyle.Render(" configuration validated.")
	case APIKeyInputStateError:
		return errorStyle.Render("Invalid ") + accentStyle.Render(string(m.provider.Name)) + errorStyle.Render(" configuration.")
	}
	return ""
}

func (m *APIKeyInput) inputView(input *textinput.Model, focused bool) string {
	t := m.com.Styles

	switch m.state {
	case APIKeyInputStateInitial:
		input.Prompt = "> "
		input.SetStyles(t.TextInput)
		if focused {
			input.Focus()
		} else {
			input.Blur()
		}
	case APIKeyInputStateVerifying:
		ts := t.TextInput
		ts.Blurred.Prompt = ts.Focused.Prompt
		input.Prompt = m.spinner.View()
		input.SetStyles(ts)
		input.Blur()
	case APIKeyInputStateVerified:
		ts := t.TextInput
		ts.Blurred.Prompt = ts.Focused.Prompt
		input.Prompt = styles.CheckIcon + " "
		input.SetStyles(ts)
		input.Blur()
	case APIKeyInputStateError:
		ts := t.TextInput
		ts.Focused.Prompt = ts.Focused.Prompt.Foreground(charmtone.Cherry)
		input.Prompt = styles.LSPErrorIcon + " "
		input.SetStyles(ts)
		if focused {
			input.Focus()
		} else {
			input.Blur()
		}
	}
	return input.View()
}

// Cursor returns the cursor position relative to the dialog.
func (m *APIKeyInput) Cursor() *tea.Cursor {
	if m.state == APIKeyInputStateVerifying || m.state == APIKeyInputStateVerified {
		return nil
	}
	cur := InputCursor(m.com.Styles, m.focusedInput().Cursor())
	if cur == nil {
		return nil
	}
	cur.Y += m.cursorYOffset()
	return cur
}

func (m *APIKeyInput) cursorYOffset() int {
	// InputCursor accounts for the dialog frame and title, but this dialog also
	// renders a label line above each input plus the optional API key note block.
	offset := 1
	if !m.hasAPIKey || m.focused == 0 {
		return offset
	}
	offset += 2
	if m.existingAPIKey != "" {
		offset++
	}
	offset++
	return offset
}

// FullHelp returns the full help view.
func (m *APIKeyInput) FullHelp() [][]key.Binding {
	row := []key.Binding{m.keyMap.Submit}
	if m.fieldCount() > 1 {
		row = append(row, m.keyMap.Next, m.keyMap.Previous)
	}
	if m.hasAPIKey {
		row = append(row, m.keyMap.Reveal)
	}
	row = append(row, m.keyMap.Close)
	return [][]key.Binding{row}
}

// ShortHelp returns the short help view.
func (m *APIKeyInput) ShortHelp() []key.Binding {
	help := []key.Binding{m.keyMap.Submit}
	if m.fieldCount() > 1 {
		help = append(help, m.keyMap.Next)
	}
	if m.hasAPIKey {
		help = append(help, m.keyMap.Reveal)
	}
	help = append(help, m.keyMap.Close)
	return help
}

func (m *APIKeyInput) verifyProviderConfig() tea.Msg {
	start := time.Now()

	apiKey := m.apiKeyValue()
	baseURL := m.baseURLValue()
	providerID := string(m.provider.ID)

	var err error
	switch {
	case m.hasAPIKey && strings.TrimSpace(apiKey) == "":
		err = fmt.Errorf("an API key is required for %s", m.provider.Name)
	case strings.TrimSpace(baseURL) == "":
		err = fmt.Errorf("a base URL is required for %s", m.provider.Name)
	default:
		providerConfig := config.ProviderConfig{
			ID:      providerID,
			Name:    m.provider.Name,
			APIKey:  apiKey,
			Type:    m.provider.Type,
			BaseURL: baseURL,
		}
		err = providerConfig.TestConnection(m.com.Workspace.Resolver())
	}

	elapsed := time.Since(start)
	minimum := 750 * time.Millisecond
	if elapsed < minimum {
		time.Sleep(minimum - elapsed)
	}

	if err == nil {
		return ActionChangeAPIKeyState{State: APIKeyInputStateVerified}
	}
	return ActionChangeAPIKeyState{State: APIKeyInputStateError, Error: err}
}

func (m *APIKeyInput) saveKeyAndContinue() Action {
	providerID := string(m.provider.ID)
	baseURL := m.baseURLValue()
	if err := m.com.Workspace.SetConfigField(config.ScopeGlobal, fmt.Sprintf("providers.%s.base_url", providerID), baseURL); err != nil {
		return ActionCmd{util.ReportError(fmt.Errorf("failed to save base URL: %w", err))}
	}

	if enteredKey := strings.TrimSpace(m.apiKeyInput.Value()); m.hasAPIKey && enteredKey != "" {
		if err := m.com.Workspace.SetProviderAPIKey(config.ScopeGlobal, providerID, enteredKey); err != nil {
			return ActionCmd{util.ReportError(fmt.Errorf("failed to save API key: %w", err))}
		}
	}

	if m.model.Model == "" {
		return ActionOpenModels{PreferredProviderID: providerID}
	}

	return ActionSelectModel{
		Provider:  m.provider,
		Model:     m.model,
		ModelType: m.modelType,
	}
}

package dialog

import (
	"fmt"
	"net/http"
	"os"
	"path/filepath"

	tea "charm.land/bubbletea/v2"
	"charm.land/catwalk/pkg/catwalk"
	"github.com/KPO-Tech/seshat-runtime/pkg/doctor"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/commands"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/config"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/message"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/oauth"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/permission"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/planreview"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/session"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/skills"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/common"
	"github.com/KPO-Tech/seshat-runtime/tui/internal/seshattui/ui/util"
)

// ActionClose is a message to close the current dialog.
type ActionClose struct{}

// ActionQuit is a message to quit the application.
type ActionQuit = tea.QuitMsg

// ActionOpenDialog is a message to open a dialog.
type ActionOpenDialog struct {
	DialogID string
}

// ActionOpenDoctor opens or refreshes the doctor diagnostics dialog.
type ActionOpenDoctor struct {
	Report doctor.Report
}

// ActionOpenModels opens the models dialog, optionally focused on a provider.
type ActionOpenModels struct {
	PreferredProviderID string
}

// ActionOpenWebSearchConfig opens the web-search configuration dialog.
type ActionOpenWebSearchConfig struct {
	ProviderID string
}

// ActionSelectWebSearchProvider selects the active web-search provider mode.
type ActionSelectWebSearchProvider struct {
	ProviderID string
}

// ActionSelectCapabilityProvider selects the provider used by a capability-specific tool.
type ActionSelectCapabilityProvider struct {
	Capability string
	ProviderID string
}

// ActionSelectSession is a message indicating a session has been selected.
type ActionSelectSession struct {
	Session session.Session
}

// ActionSelectModel is a message indicating a model has been selected.
type ActionSelectModel struct {
	Provider       catwalk.Provider
	Model          config.SelectedModel
	ModelType      config.SelectedModelType
	ReAuthenticate bool
}

// Messages for commands
type (
	ActionNewSession        struct{}
	ActionToggleHelp        struct{}
	ActionToggleCompactMode struct{}
	ActionToggleThinking    struct{}
	ActionTogglePills       struct{}
	ActionExternalEditor    struct{}
	ActionToggleYoloMode    struct{}
	// ActionTogglePlan enters or leaves plan mode on the live session, on the
	// user's own initiative. See workspace.SetPlanMode.
	ActionTogglePlan              struct{}
	ActionToggleNotifications     struct{}
	ActionSelectNotificationStyle struct {
		Style string
	}
	ActionToggleTransparentBackground struct{}
	ActionToggleMemory                struct{}
	ActionInitializeProject           struct{}
	ActionSummarize                   struct {
		SessionID string
	}
	// ActionSelectReasoningEffort is a message indicating a reasoning effort
	// has been selected.
	ActionSelectReasoningEffort struct {
		Effort string
	}
	ActionPermissionResponse struct {
		Permission permission.PermissionRequest
		Action     PermissionAction
	}
	ActionPlanReviewSubmit struct {
		Review planreview.Review
	}
	// ActionPlanReviewRequestChanges is sent when the user sends feedback to the
	// agent without approving the plan. The dialog stays open; the agent is
	// expected to revise the plan and call submit_plan again.
	ActionPlanReviewRequestChanges struct {
		Review planreview.Review
	}
	// ActionRunCustomCommand is a message to run a custom command.
	ActionRunCustomCommand struct {
		Content   string
		Arguments []commands.Argument
		Args      map[string]string // Actual argument values
		Skill     *skills.Skill     // Set when this is a skill command
	}
	// ActionMemory is a request about long-term memory: Op is "show", "undo" or "forget". Forget needs the name of a
	// note, asked for in the arguments dialog when Name is empty.
	ActionMemory struct {
		Op   string
		Name string
	}
	// ActionAttachSkill is sent when a skill is selected from the commands
	// dialog to be attached to the conversation as a markdown attachment.
	ActionAttachSkill struct {
		ID   string
		Name string
	}
	// ActionRunMCPPrompt is a message to run a custom command.
	ActionRunMCPPrompt struct {
		Title       string
		Description string
		PromptID    string
		ClientID    string
		Arguments   []commands.Argument
		Args        map[string]string // Actual argument values
	}
	// ActionEnableDockerMCP is a message to enable Docker MCP.
	ActionEnableDockerMCP struct{}
	// ActionDisableDockerMCP is a message to disable Docker MCP.
	ActionDisableDockerMCP struct{}
	// ActionEnableMCPServer reconnects/enables a named MCP server from Settings.
	ActionEnableMCPServer struct{ Name string }
	// ActionDisableMCPServer disconnects/disables a named MCP server from Settings.
	ActionDisableMCPServer struct{ Name string }
	// ActionCopyLastMessage copies the user's last sent message to the clipboard.
	ActionCopyLastMessage struct{}
	// ActionToggleVerboseSteps toggles verbose agent step narration.
	ActionToggleVerboseSteps struct{}
	// ActionOpenProviderConfig triggers the API key / auth flow for a provider.
	ActionOpenProviderConfig struct {
		Provider catwalk.Provider
	}
)

// Messages for API key input dialog.
type (
	ActionChangeAPIKeyState struct {
		State APIKeyInputState
		Error error
	}
)

// Messages for OAuth2 device flow dialog.
type (
	// ActionInitiateOAuth is sent when the device auth is initiated
	// successfully.
	ActionInitiateOAuth struct {
		DeviceCode      string
		UserCode        string
		ExpiresIn       int
		VerificationURL string
		Interval        int
	}

	// ActionCompleteOAuth is sent when the device flow completes successfully.
	ActionCompleteOAuth struct {
		Token *oauth.Token
	}

	// ActionOAuthErrored is sent when the device flow encounters an error.
	ActionOAuthErrored struct {
		Error error
	}
)

// ActionCmd represents an action that carries a [tea.Cmd] to be passed to the
// Bubble Tea program loop.
type ActionCmd struct {
	Cmd tea.Cmd
}

// ActionFilePickerSelected is a message indicating a file has been selected in
// the file picker dialog.
type ActionFilePickerSelected struct {
	Path string
}

// Cmd returns a command that reads the file at path and sends a
// [message.Attachement] to the program.
func (a ActionFilePickerSelected) Cmd() tea.Cmd {
	path := a.Path
	if path == "" {
		return nil
	}
	return func() tea.Msg {
		isFileLarge, err := common.IsFileTooBig(path, common.MaxAttachmentSize)
		if err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("unable to read the image: %v", err),
			}
		}
		if isFileLarge {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  "file too large, max 5MB",
			}
		}

		content, err := os.ReadFile(path)
		if err != nil {
			return util.InfoMsg{
				Type: util.InfoTypeError,
				Msg:  fmt.Sprintf("unable to read the image: %v", err),
			}
		}

		mimeBufferSize := min(512, len(content))
		mimeType := http.DetectContentType(content[:mimeBufferSize])
		fileName := filepath.Base(path)

		return message.Attachment{
			FilePath: path,
			FileName: fileName,
			MimeType: mimeType,
			Content:  content,
		}
	}
}

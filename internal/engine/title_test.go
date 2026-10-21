package engine

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/KPO-Tech/seshat-runtime/internal/providers"
	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

func newTitleClient(t *testing.T, handler http.HandlerFunc) *providers.Client {
	t.Helper()
	server := httptest.NewServer(handler)
	t.Cleanup(server.Close)
	return providers.NewClientWithConfig("test-key", &providers.Config{Provider: types.APIProviderOpenAI, BaseURL: server.URL})
}

func titleAnswer(w http.ResponseWriter, text string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"id": "t", "model": "m",
		"choices": []map[string]any{{"index": 0, "finish_reason": "stop", "message": map[string]any{"role": "assistant", "content": text}}},
		"usage":   map[string]any{"prompt_tokens": 5, "completion_tokens": 3, "total_tokens": 8},
	})
}

func TestTitleRequestGivesADedicatedModelAShortBudgetAndAStopAtTheEndOfTheLine(t *testing.T) {
	model := types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "m"}

	dedicated := titleRequest(titleTarget{model: model, dedicated: true}, "hello")
	if dedicated.MaxTokens != dedicatedTitleMaxTokens || len(dedicated.StopSequences) != 1 || dedicated.StopSequences[0] != "\n" {
		t.Fatalf("a dedicated title model must get a short budget and stop at the end of the line, got %d %q", dedicated.MaxTokens, dedicated.StopSequences)
	}

	chat := titleRequest(titleTarget{model: model}, "hello")
	if chat.MaxTokens != reasoningTitleMaxTokens || len(chat.StopSequences) != 0 {
		t.Fatalf("the chat model may reason before the title: expected room and no stop at a newline, got %d %q", chat.MaxTokens, chat.StopSequences)
	}
	if dedicated.MaxTokens >= chat.MaxTokens {
		t.Fatal("the dedicated budget must be the short one")
	}
}

func TestTitleTargetsTryTheDedicatedModelThenTheChatModel(t *testing.T) {
	small := providers.NewClientWithConfig("k", &providers.Config{Provider: types.APIProviderOpenAI, BaseURL: "http://127.0.0.1:1"})
	chat := providers.NewClientWithConfig("k", &providers.Config{Provider: types.APIProviderOpenAI, BaseURL: "http://127.0.0.1:2"})
	chatModel := types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "chat"}
	smallModel := types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "small"}

	both := (&Engine{apiClient: chat, titleAPIClient: small, config: &Config{Model: chatModel, TitleModel: smallModel}}).titleTargets()
	if len(both) != 2 || !both[0].dedicated || both[0].model.Model != "small" || both[1].dedicated || both[1].model.Model != "chat" {
		t.Fatalf("expected the dedicated model first and the chat model after, got %+v", both)
	}

	onlyChat := (&Engine{apiClient: chat, config: &Config{Model: chatModel}}).titleTargets()
	if len(onlyChat) != 1 || onlyChat[0].dedicated {
		t.Fatalf("without a dedicated model only the chat model is tried, got %+v", onlyChat)
	}

	same := (&Engine{apiClient: chat, titleAPIClient: chat, config: &Config{Model: chatModel}}).titleTargets()
	if len(same) != 1 {
		t.Fatalf("one client is not asked twice, got %+v", same)
	}
}

func TestATitleStillArrivesWhenTheDedicatedModelFails(t *testing.T) {
	var smallSaw map[string]any
	small := newTitleClient(t, func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(raw, &smallSaw)
		http.Error(w, "bad request", http.StatusBadRequest)
	})
	chat := newTitleClient(t, func(w http.ResponseWriter, r *http.Request) { titleAnswer(w, "\"Wiring a backend gateway.\"") })

	titled := make(chan string, 1)
	var once sync.Once
	engine := &Engine{
		apiClient: chat, titleAPIClient: small,
		config:          &Config{Model: types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "chat"}, TitleModel: types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "small"}},
		onSessionTitled: func(_ types.SessionID, title string) { once.Do(func() { titled <- title }) },
	}
	go engine.generateTitleAsync("s1", "how do I wire a backend gateway?")

	select {
	case title := <-titled:
		if title != "Wiring a backend gateway" {
			t.Fatalf("expected the cleaned title of the chat model, got %q", title)
		}
	case <-time.After(10 * time.Second):
		t.Fatal("no title: the chat model was never tried after the dedicated model failed")
	}
	if got, _ := smallSaw["max_tokens"].(float64); int(got) != dedicatedTitleMaxTokens {
		t.Fatalf("the dedicated model was sent max_tokens %v, want %d", smallSaw["max_tokens"], dedicatedTitleMaxTokens)
	}
	if stop, _ := smallSaw["stop"].([]any); len(stop) != 1 || stop[0] != "\n" {
		t.Fatalf("the dedicated model was not told to stop at the end of the line: %v", smallSaw["stop"])
	}
}

func TestATitleFromTheDedicatedModelIsTakenWithoutAskingTheChatModel(t *testing.T) {
	small := newTitleClient(t, func(w http.ResponseWriter, r *http.Request) { titleAnswer(w, "Calculator and TCP congestion") })
	chat := newTitleClient(t, func(w http.ResponseWriter, r *http.Request) {
		t.Error("the chat model must not be asked when the dedicated model answered")
		titleAnswer(w, "unused")
	})
	titled := make(chan string, 1)
	engine := &Engine{
		apiClient: chat, titleAPIClient: small,
		config:          &Config{Model: types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "chat"}, TitleModel: types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "small"}},
		onSessionTitled: func(_ types.SessionID, title string) { titled <- title },
	}
	engine.generateTitleAsync("s1", "use the calculator then explain TCP")
	select {
	case title := <-titled:
		if title != "Calculator and TCP congestion" {
			t.Fatalf("got %q", title)
		}
	default:
		t.Fatal("no title from the dedicated model")
	}
}

func TestTheFirstMessageIsHandedOverAsTextToTitleNotAsAnInstruction(t *testing.T) {
	got := titleUserMessage("explain TCP in 300 words")
	if !strings.Contains(got, "Do not answer it") || !strings.Contains(got, "\"\"\"\nexplain TCP in 300 words\n\"\"\"") {
		t.Fatalf("the message must be framed as text to title, got %q", got)
	}
	req := titleRequest(titleTarget{dedicated: true}, "explain TCP in 300 words")
	if text := req.Messages[0].Content[0].(types.TextContent).Text; text != titleUserMessage("explain TCP in 300 words") {
		t.Fatalf("the request must carry the framed message, got %q", text)
	}
}

func TestTheConfiguredTitleModelIsAskedOnTheChatClientBeforeTheChatModel(t *testing.T) {
	chat := providers.NewClientWithConfig("k", &providers.Config{Provider: types.APIProviderOpenAI, BaseURL: "http://127.0.0.1:2"})
	chatModel := types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "chat"}
	titleModel := types.ModelIdentifier{Provider: types.APIProviderOpenAI, Model: "cheap"}

	targets := (&Engine{apiClient: chat, config: &Config{Model: chatModel, TitleModel: titleModel}}).titleTargets()
	if len(targets) != 2 || targets[0].model.Model != "cheap" || targets[1].model.Model != "chat" {
		t.Fatalf("expected the title model then the chat model on the chat client, got %+v", targets)
	}
	if targets[0].dedicated {
		t.Fatal("a title model on the chat client is not known to be small: it keeps the budget that leaves room to reason")
	}
}

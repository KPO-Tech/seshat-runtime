package providers

import (
	"strings"
	"sync"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// thinkingLocked holds the models that refused to have their reasoning switched off, so that the request is not sent a second time with a
// parameter they reject. z.ai answers HTTP 400 "This model always engages in thinking and cannot be disabled" for glm-5.3 (the glm-4.5, 4.6, 5 and
// 5.1 models accept it). Learned from the answer instead of listed: a new model of that kind needs no change here.
var thinkingLocked sync.Map

func thinkingLockKey(model types.ModelIdentifier) string {
	return string(model.Provider) + "/" + model.Model
}

func thinkingIsLocked(model types.ModelIdentifier) bool {
	_, locked := thinkingLocked.Load(thinkingLockKey(model))
	return locked
}

// cannotDisableThinking reports whether a provider refused a request because the model always reasons.
func cannotDisableThinking(err error) bool {
	if err == nil {
		return false
	}
	text := strings.ToLower(err.Error())
	return strings.Contains(text, "cannot be disabled") || strings.Contains(text, "always engages in thinking")
}

// createMessageWithoutThinkingSwitch sends the request, and when the provider says the model cannot stop reasoning, remembers it and sends the
// request again without the switch: the caller (the permission classifier, the summary of a long conversation) gets an answer instead of an error.
// The caller keeps its own output budget, which already leaves room for the reasoning.
func (c *Client) createMessageWithoutThinkingSwitch(req types.APIRequest, send func(types.APIRequest) (*types.APIResponse, error)) (*types.APIResponse, error) {
	resp, err := send(req)
	if err != nil && req.DisableThinking && cannotDisableThinking(err) {
		thinkingLocked.Store(thinkingLockKey(req.Model), struct{}{})
		retry := req
		retry.DisableThinking = false
		return send(retry)
	}
	return resp, err
}

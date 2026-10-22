package engine

import (
	"fmt"
	"regexp"

	"github.com/KPO-Tech/seshat-runtime/internal/types"
)

// minBinaryPayloadChars is the size from which a base64 field of a tool result is left out of what the model reads.
const minBinaryPayloadChars = 2048

// base64FieldPattern finds a JSON string field whose name ends in _base64: an image or a screenshot a tool returned for the interface to
// draw. Only a long value is left out (the regular expression engine does not count that far).
var base64FieldPattern = regexp.MustCompile(`"([A-Za-z0-9_]*_base64)"\s*:\s*"[A-Za-z0-9+/=]+"`)

// withoutBinaryPayloads returns the messages as the model should read them: a long base64 field of a tool result is replaced by a note,
// so an image of a megabyte does not cost hundreds of thousands of tokens that the model cannot use. The stored messages are left as
// they are, the interface still draws the image from them. A message that holds no such field is returned as it was.
func withoutBinaryPayloads(messages []types.Message) []types.Message {
	var out []types.Message
	for i, message := range messages {
		content, changed := stripBinaryPayloadBlocks(message.Content)
		if !changed {
			continue
		}
		if out == nil {
			out = append([]types.Message(nil), messages...)
		}
		message.Content = content
		out[i] = message
	}
	if out == nil {
		return messages
	}
	return out
}

func stripBinaryPayloadBlocks(blocks []types.ContentBlock) ([]types.ContentBlock, bool) {
	var out []types.ContentBlock
	for i, block := range blocks {
		result, ok := block.(types.ToolResultContent)
		if !ok || len(result.Content) < minBinaryPayloadChars {
			continue
		}
		stripped := base64FieldPattern.ReplaceAllStringFunc(result.Content, func(match string) string {
			if len(match) < minBinaryPayloadChars {
				return match
			}
			name := base64FieldPattern.FindStringSubmatch(match)[1]
			return fmt.Sprintf("%q:%q", name, fmt.Sprintf("[%d characters of image data left out: the person sees the image, you do not need it]", len(match)))
		})
		if stripped == result.Content {
			continue
		}
		if out == nil {
			out = append([]types.ContentBlock(nil), blocks...)
		}
		result.Content = stripped
		out[i] = result
	}
	if out == nil {
		return blocks, false
	}
	return out, true
}

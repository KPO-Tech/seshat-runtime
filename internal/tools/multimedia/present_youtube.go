package multimedia

import (
	"encoding/json"
	"fmt"
	"net/url"
	"regexp"
	"strings"

	tool "github.com/KPO-Tech/seshat-runtime/internal/tools/registry"
)

var youTubeIDPattern = regexp.MustCompile(`^[A-Za-z0-9_-]{11}$`)

// youTubeVideoID returns the id of the video a YouTube link points to (watch, short link, embed, shorts and live forms), or false for
// any other address: only a YouTube link is ever handed to the interface to embed.
func youTubeVideoID(raw string) (string, bool) {
	parsed, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || (parsed.Scheme != "https" && parsed.Scheme != "http") {
		return "", false
	}
	host := strings.TrimPrefix(strings.ToLower(parsed.Hostname()), "www.")
	var id string
	switch host {
	case "youtu.be":
		id = strings.Trim(parsed.Path, "/")
	case "youtube.com", "m.youtube.com", "music.youtube.com", "youtube-nocookie.com":
		if parsed.Path == "/watch" {
			id = parsed.Query().Get("v")
			break
		}
		for _, prefix := range []string{"/embed/", "/shorts/", "/live/", "/v/"} {
			if strings.HasPrefix(parsed.Path, prefix) {
				id = strings.Trim(strings.TrimPrefix(parsed.Path, prefix), "/")
				break
			}
		}
	default:
		return "", false
	}
	if !youTubeIDPattern.MatchString(id) {
		return "", false
	}
	return id, true
}

func presentYouTube(link string, parsed map[string]any) (tool.CallResult, error) {
	id, ok := youTubeVideoID(link)
	if !ok {
		return tool.NewErrorResult(fmt.Errorf("present_artifact: %q is not a YouTube video link", link)), nil
	}
	title, _ := parsed["title"].(string)
	if strings.TrimSpace(title) == "" {
		title = "YouTube video"
	}
	result := map[string]any{
		"kind":     "youtube",
		"video_id": id,
		"url":      "https://www.youtube.com/watch?v=" + id,
		"title":    title,
	}
	if summary, _ := parsed["summary"].(string); strings.TrimSpace(summary) != "" {
		result["summary"] = summary
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		return tool.NewErrorResult(err), nil
	}
	return tool.CallResult{Data: result, ContentType: tool.ContentTypeJSON, Content: string(encoded)}, nil
}

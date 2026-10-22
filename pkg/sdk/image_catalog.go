package sdk

import imageproviders "github.com/KPO-Tech/seshat-runtime/internal/image/providers"

// ImageProviderInfo describes an image generation provider the runtime knows: the identifier a host gives in ImageGenerationConfig.Provider,
// a name to show, and its models, the first being the default one.
type ImageProviderInfo struct {
	ID          string   `json:"id"`
	DisplayName string   `json:"display_name"`
	Models      []string `json:"models"`
}

// ImageProviders lists the image generation providers this build knows, so a host can offer their models to a person.
func ImageProviders() []ImageProviderInfo {
	profiles := imageproviders.Profiles()
	out := make([]ImageProviderInfo, 0, len(profiles))
	for _, profile := range profiles {
		out = append(out, ImageProviderInfo{ID: profile.ID, DisplayName: profile.DisplayName, Models: append([]string(nil), profile.Models...)})
	}
	return out
}

package common

import (
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
)

var newAPIGeminiImageModel = regexp.MustCompile(`^(?:gemini-3(?:\.1)?-(?:[\w.-]+-)?image(?:-[\w.-]+)?|(?:gemini-)?nano-banana(?:-[\w.-]+)?)$`)

// Match the legacy canvas presets explicitly. Other pixel dimensions can be
// valid native Gemini sizes and must retain the caller's requested resolution.
var newAPILegacyCanvasImageSizes = map[string]struct{ ratio, quality string }{
	"1024x1024": {"1:1", "1K"}, "1024x1536": {"2:3", "1K"}, "1536x1024": {"3:2", "1K"},
	"1024x768": {"4:3", "1K"}, "768x1024": {"3:4", "1K"}, "1536x864": {"16:9", "1K"},
	"864x1536": {"9:16", "1K"}, "2016x864": {"21:9", "1K"}, "864x2016": {"9:16", "1K"},
	"2048x2048": {"1:1", "2K"}, "1360x2048": {"2:3", "2K"}, "2048x1360": {"3:2", "2K"},
	"2048x1536": {"4:3", "2K"}, "1536x2048": {"3:4", "2K"}, "2048x1152": {"16:9", "2K"},
	"1152x2048": {"9:16", "2K"}, "2688x1152": {"21:9", "2K"}, "1152x2688": {"9:16", "2K"},
	"2880x2880": {"1:1", "4K"}, "2336x3520": {"2:3", "4K"}, "3520x2336": {"3:2", "4K"},
	"3312x2480": {"4:3", "4K"}, "2480x3312": {"3:4", "4K"}, "3840x2160": {"16:9", "4K"},
	"2160x3840": {"9:16", "4K"}, "3840x1648": {"21:9", "4K"}, "1648x3840": {"9:16", "4K"},
}

// NormalizeNewAPIImageRequest translates legacy canvas parameters only for the
// NewAPI Gemini Images protocol. Callers apply this to an outbound copy so that
// billing expressions continue to see the original client parameters.
func NormalizeNewAPIImageRequest(channelType int, request *dto.ImageRequest) bool {
	if channelType != constant.ChannelTypeNewAPI || request == nil {
		return false
	}
	model := strings.ToLower(strings.TrimSpace(request.Model))
	if slash := strings.LastIndexByte(model, '/'); slash >= 0 {
		model = model[slash+1:]
	}
	if !newAPIGeminiImageModel.MatchString(model) {
		return false
	}

	quality := strings.ToUpper(strings.TrimSpace(request.Quality))
	switch quality {
	case "", "AUTO", "LOW", "MEDIUM", "HIGH", "XHIGH", "MAX":
		request.Quality = ""
	case "1K", "2K", "4K":
		request.Quality = quality
	}
	size := strings.ToLower(strings.TrimSpace(request.Size))
	if legacy, ok := newAPILegacyCanvasImageSizes[size]; ok {
		request.Size = legacy.ratio
		if request.Quality == "" {
			request.Quality = legacy.quality
		}
		return true
	}
	switch size {
	case "", "auto":
		request.Size = ""
	case "1k", "2k", "4k":
		request.Size = ""
		if request.Quality == "" {
			request.Quality = strings.ToUpper(size)
		}
	case "9:21":
		request.Size = "9:16"
	}
	return true
}

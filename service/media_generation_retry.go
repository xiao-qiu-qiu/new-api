package service

import (
	"net/http"
	"regexp"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/pkg/jsplugin"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/model_setting"
	"github.com/gin-gonic/gin"
	"github.com/tidwall/gjson"
)

const (
	mediaGenerationRequestKey    = "media_generation_request"
	MediaGenerationNoRetryReason = "media_generation_no_retry"
	MediaGenerationNoRetrySource = "request_type"
)

// These are generation families missing from common.ImageGenerationModels.
// Match identities, never arbitrary occurrences of "image" or "video".
var mediaGenerationModelPattern = regexp.MustCompile(`^(?:(?:doubao-)?seed(?:ream|ance)(?:[-_.]|$)|gemini-[0-9.]+-(?:flash|pro)-image(?:[-_.]|$)|nano-banana(?:[-_.]|$)|chatgpt-image(?:[-_.]|$)|grok-(?:2-image|imagine-(?:image|video))(?:[-_.]|$)|sora(?:[-_.]|$)|veo(?:[-_.]|$)|kling(?:[-_.]|$)|(?:minimax-)?hailuo(?:[-_.]|$)|vidu(?:[-_.]|$)|jimeng_vgfm_|wanx?[0-9.]+-(?:t2v|i2v|r2v|s2v|animate|imageedit|i2i)(?:[-_.]|$)|hunyuan-video(?:[-_.]|$))`)

// MarkMediaGenerationRequest records generation intent before an upstream attempt
// and again after model mapping. A later conversion cannot clear an earlier match.
// Generic tasks share the VideoSubmit mode with music/lyrics; that mode alone is
// therefore insufficient for RelayFormatTask.
func MarkMediaGenerationRequest(c *gin.Context, info *relaycommon.RelayInfo) {
	if c == nil || info == nil || IsMediaGenerationRequest(c) {
		return
	}
	if info.RelayFormat == types.RelayFormatOpenAIImage {
		c.Set(mediaGenerationRequestKey, true)
		return
	}
	switch info.RelayMode {
	case relayconstant.RelayModeImagesGenerations, relayconstant.RelayModeImagesEdits,
		relayconstant.RelayModeMidjourneyImagine, relayconstant.RelayModeMidjourneyBlend,
		relayconstant.RelayModeMidjourneyChange, relayconstant.RelayModeMidjourneySimpleChange,
		relayconstant.RelayModeMidjourneyAction, relayconstant.RelayModeMidjourneyModal,
		relayconstant.RelayModeSwapFace, relayconstant.RelayModeMidjourneyVideo,
		relayconstant.RelayModeMidjourneyEdits:
		c.Set(mediaGenerationRequestKey, true)
		return
	case relayconstant.RelayModeVideoSubmit:
		if info.RelayFormat != types.RelayFormatTask && info.TaskRelayInfo == nil {
			c.Set(mediaGenerationRequestKey, true)
			return
		}
	}
	if c.Request != nil && c.Request.URL != nil && c.Request.Method == http.MethodPost {
		path := c.Request.URL.Path
		if path == "/v1/videos" || strings.HasPrefix(path, "/v1/videos/") && strings.HasSuffix(path, "/remix") {
			c.Set(mediaGenerationRequestKey, true)
			return
		}
	}
	if info.TaskRelayInfo != nil {
		switch info.Action {
		case constant.TaskActionTextToVideo, constant.TaskActionImageToVideo,
			constant.TaskActionFirstTailToVideo, constant.TaskActionReferenceToVideo,
			"text_to_image", "image_to_image":
			c.Set(mediaGenerationRequestKey, true)
			return
		}
	}

	switch request := info.Request.(type) {
	case *dto.OpenAIResponsesRequest:
		if request != nil && hasMediaGenerationTools(request.Tools) {
			c.Set(mediaGenerationRequestKey, true)
			return
		}
	case *dto.GeneralOpenAIRequest:
		if request != nil {
			var modalities []string
			_ = common.Unmarshal(request.Modalities, &modalities)
			if hasMediaOutputModalities(modalities) || common.GetJsonType(request.ImageConfig) == "object" {
				c.Set(mediaGenerationRequestKey, true)
				return
			}
			for _, tool := range request.Tools {
				if len(tool.Native) > 0 {
					var native struct {
						Type string `json:"type"`
					}
					if common.Unmarshal(tool.Native, &native) != nil {
						continue
					}
					tool.Type = native.Type
				}
				if tool.Type == dto.BuildInToolImageGeneration || tool.Type == "video_generation" {
					c.Set(mediaGenerationRequestKey, true)
					return
				}
			}
		}
	case *dto.GeminiChatRequest:
		if request != nil && (hasMediaOutputModalities(request.GenerationConfig.ResponseModalities) || common.GetJsonType(request.GenerationConfig.ImageConfig) == "object") {
			c.Set(mediaGenerationRequestKey, true)
			return
		}
	}

	if value, ok := c.Get(jsplugin.ContextKeyPinnedEndpoint); ok {
		if pinned, ok := value.(jsplugin.PinnedEndpoint); ok && (pinned.Protocol == jsplugin.ProtocolOpenAIImage || pinned.Protocol == "openai_video") {
			c.Set(mediaGenerationRequestKey, true)
			return
		}
	}
	generation := mediaGenerationRoutingGeneration(c)
	models := []string{info.OriginModelName, c.GetString("resolved_task_model")}
	if info.ChannelMeta != nil {
		models = append(models, info.UpstreamModelName)
	}
	switch request := info.Request.(type) {
	case *dto.GeneralOpenAIRequest:
		if request != nil {
			models = append(models, request.Model)
		}
	case *dto.OpenAIResponsesRequest:
		if request != nil {
			models = append(models, request.Model)
		}
	}
	for _, name := range models {
		if isMediaGenerationModel(name, generation) {
			c.Set(mediaGenerationRequestKey, true)
			return
		}
	}
}

func IsMediaGenerationRequest(c *gin.Context) bool {
	return c != nil && c.GetBool(mediaGenerationRequestKey)
}

// MarkMediaGenerationRequestBody observes the final JSON request after channel
// overrides/conversion. It selects only output-intent fields; input media and
// prompt content are never decoded or used for classification.
func MarkMediaGenerationRequestBody(c *gin.Context, info *relaycommon.RelayInfo, body []byte) {
	MarkMediaGenerationRequest(c, info)
	if c == nil || IsMediaGenerationRequest(c) || len(body) == 0 {
		return
	}
	fields := gjson.GetManyBytes(body, "model", "tools.#.type", "modalities", "generationConfig.responseModalities", "generationConfig.imageConfig", "generation_config.response_modalities", "generation_config.image_config", "imageConfig", "image_config", "extra_body.google.image_config")
	for _, tool := range fields[1].Array() {
		if tool.Type == gjson.String && (tool.Str == dto.BuildInToolImageGeneration || tool.Str == "video_generation") {
			c.Set(mediaGenerationRequestKey, true)
			return
		}
	}
	for _, field := range fields[2:] {
		if field.IsObject() {
			c.Set(mediaGenerationRequestKey, true)
			return
		}
		for _, modality := range field.Array() {
			if modality.Type == gjson.String && (strings.EqualFold(modality.Str, "image") || strings.EqualFold(modality.Str, "video")) {
				c.Set(mediaGenerationRequestKey, true)
				return
			}
		}
	}
	if fields[0].Type == gjson.String && isMediaGenerationModel(fields[0].Str, mediaGenerationRoutingGeneration(c)) {
		c.Set(mediaGenerationRequestKey, true)
	}
}

func mediaGenerationRoutingGeneration(c *gin.Context) *jsplugin.RoutingGeneration {
	for _, key := range []string{jsplugin.ContextKeyPinnedEndpoint, jsplugin.ContextKeyPinnedRoute, jsplugin.ContextKeyPinnedPlugin} {
		value, _ := c.Get(key)
		switch pinned := value.(type) {
		case jsplugin.PinnedEndpoint:
			if pinned.Generation != nil {
				return pinned.Generation
			}
		case jsplugin.PinnedRoute:
			if pinned.Generation != nil {
				return pinned.Generation
			}
		case jsplugin.PinnedPlugin:
			if pinned.Generation != nil {
				return pinned.Generation
			}
		}
	}
	return jsplugin.DefaultRegistry.Generation()
}

func hasMediaOutputModalities(modalities []string) bool {
	for _, modality := range modalities {
		if strings.EqualFold(modality, "image") || strings.EqualFold(modality, "video") {
			return true
		}
	}
	return false
}

func hasMediaGenerationTools(raw []byte) bool {
	var tools []struct {
		Type string `json:"type"`
	}
	if common.Unmarshal(raw, &tools) != nil {
		return false
	}
	for _, tool := range tools {
		if tool.Type == dto.BuildInToolImageGeneration || tool.Type == "video_generation" {
			return true
		}
	}
	return false
}

func isMediaGenerationModel(name string, generation *jsplugin.RoutingGeneration) bool {
	if name == "" {
		return false
	}
	name = strings.TrimSpace(name)
	// Namespace prefixes are used by providers and model hubs, e.g. Google and
	// black-forest-labs. Only the model identity participates in family matching.
	family := strings.ToLower(name[strings.LastIndex(name, "/")+1:])
	if common.IsImageGenerationModel(family) || model_setting.IsGeminiModelSupportImagine(family) || mediaGenerationModelPattern.MatchString(family) {
		return true
	}
	for _, endpoint := range model.GetModelSupportEndpointTypes(name) {
		if endpoint == constant.EndpointTypeImageGeneration || endpoint == constant.EndpointTypeOpenAIVideo {
			return true
		}
	}
	if canonical, ok := generation.CanonicalModel(name); ok {
		for _, path := range []string{"/v1/images/generations", "/v1/videos"} {
			// The generation's endpoint index applies ProtocolClaim.Models, so a
			// mixed image/video/audio plugin never classifies all of its models alike.
			if _, ok := generation.LookupEndpoint(http.MethodPost, path, canonical); ok {
				return true
			}
		}
	}
	return false
}

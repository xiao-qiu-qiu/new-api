package newapi_test

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relay"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	relayconstant "github.com/QuantumNous/new-api/relay/constant"
	"github.com/QuantumNous/new-api/relay/helper"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/billing_setting"
	"github.com/QuantumNous/new-api/setting/config"
	"github.com/QuantumNous/new-api/setting/operation_setting"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// Exercise ImageHelper's real transport, not just the parameter normalizer:
// multipart edits previously copied raw form fields over the normalized DTO.
func TestGeminiImageCompatibilityOutbound(t *testing.T) {
	gin.SetMode(gin.TestMode)
	service.InitHttpClient()
	previousPrices := ratio_setting.ModelPrice2JSONString()
	prices := ratio_setting.GetModelPriceCopy()
	for _, model := range []string{"gemini-nano-banana-2.1", "gemini-3-pro-image-preview", "gemini-3.1-flash-image-preview", "google/gemini-3.1-flash-image-preview", "nano-banana-pro", "canvas-image", "gpt-image-2"} {
		prices[model] = 0
	}
	encodedPrices, err := common.Marshal(prices)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(string(encodedPrices)))
	quotaSettings := operation_setting.GetQuotaSetting()
	previousFreePreconsume := quotaSettings.EnableFreeModelPreConsume
	quotaSettings.EnableFreeModelPreConsume = false
	t.Cleanup(func() {
		quotaSettings.EnableFreeModelPreConsume = previousFreePreconsume
		require.NoError(t, ratio_setting.UpdateModelPriceByJSONString(previousPrices))
	})

	for _, tc := range []struct {
		name, model, size, quality, wantSize, wantQuality string
		mapping                                           string
		passthrough, ordinaryChannel                      bool
	}{
		{name: "old landscape 4K", size: "3840x2160", quality: "high", wantSize: "16:9", wantQuality: "4K"},
		{name: "old square 4K", size: "2880x2880", quality: "max", wantSize: "1:1", wantQuality: "4K"},
		{name: "old 1K preset", size: "2016x864", quality: "xhigh", wantSize: "21:9", wantQuality: "1K"},
		{name: "old 2K preset", size: "2048x1152", quality: "medium", wantSize: "16:9", wantQuality: "2K"},
		{name: "explicit K wins", size: "3840x2160", quality: "1K", wantSize: "16:9", wantQuality: "1K"},
		{name: "existing ratio and K", model: "gemini-3-pro-image-preview", size: "4:5", quality: "2K", wantSize: "4:5", wantQuality: "2K"},
		{name: "Gemini 3.1 namespace", model: "google/gemini-3.1-flash-image-preview", size: "3:2", quality: "4K", wantSize: "3:2", wantQuality: "4K"},
		{name: "unsupported portrait ratio", size: "9:21", quality: "4K", wantSize: "9:16", wantQuality: "4K"},
		{name: "auto omitted", size: "auto", quality: "auto"},
		{name: "legacy quality without pixels omitted", model: "nano-banana-pro", size: "16:9", quality: "low", wantSize: "16:9"},
		{name: "resolution in size", size: "4K", wantQuality: "4K"},
		{name: "native 1K pixels preserved", size: "1584x672", quality: "auto", wantSize: "1584x672"},
		{name: "native 2K pixels preserved", size: "1792x2400", quality: "2K", wantSize: "1792x2400", wantQuality: "2K"},
		{name: "native wide 2K pixels preserved", size: "3168x1344", quality: "auto", wantSize: "3168x1344"},
		{name: "additional native aspect ratio preserved", size: "1:4", quality: "4K", wantSize: "1:4", wantQuality: "4K"},
		{name: "mapped alias", model: "canvas-image", mapping: `{"canvas-image":"gemini-nano-banana-2.1"}`, size: "3840x2160", quality: "high", wantSize: "16:9", wantQuality: "4K"},
		{name: "passthrough", size: "3840x2160", quality: "high", passthrough: true, wantSize: "16:9", wantQuality: "4K"},
		{name: "ordinary GPT unchanged", model: "gpt-image-2", size: "3840x2160", quality: "high", wantSize: "3840x2160", wantQuality: "high"},
		{name: "ordinary OpenAI channel unchanged", model: "gemini-3.1-flash-image-preview", size: "3840x2160", quality: "high", ordinaryChannel: true, wantSize: "3840x2160", wantQuality: "high"},
	} {
		for _, format := range []string{"generation JSON", "edit JSON", "edit multipart"} {
			t.Run(tc.name+"/"+format, func(t *testing.T) {
				model := tc.model
				if model == "" {
					model = "gemini-nano-banana-2.1"
				}
				mode, path := relayconstant.RelayModeImagesGenerations, "/v1/images/generations"
				if format != "generation JSON" {
					mode, path = relayconstant.RelayModeImagesEdits, "/v1/images/edits"
				}
				var body bytes.Buffer
				contentType := "application/json"
				if format == "edit multipart" {
					writer := multipart.NewWriter(&body)
					for key, value := range map[string]string{"model": model, "prompt": "fixture", "size": tc.size, "quality": tc.quality, "n": strconv.Itoa(dto.MaxImageN), "response_format": "b64_json"} {
						require.NoError(t, writer.WriteField(key, value))
					}
					part, err := writer.CreateFormFile("image", "input.png")
					require.NoError(t, err)
					_, err = part.Write([]byte("image fixture"))
					require.NoError(t, err)
					require.NoError(t, writer.Close())
					contentType = writer.FormDataContentType()
				} else {
					_, err := fmt.Fprintf(&body, `{"model":%q,"prompt":"fixture","size":%q,"quality":%q,"n":%d,"response_format":"b64_json","vendor_extension":true}`, model, tc.size, tc.quality, dto.MaxImageN)
					require.NoError(t, err)
				}
				type observedRequest struct {
					fields map[string]any
					image  string
					err    error
				}
				observed := make(chan observedRequest, 1)
				upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					result := observedRequest{fields: map[string]any{}}
					if strings.Contains(r.Header.Get("Content-Type"), "multipart/form-data") {
						result.err = r.ParseMultipartForm(1 << 20)
						if result.err == nil {
							defer r.MultipartForm.RemoveAll()
							for key, values := range r.PostForm {
								result.fields[key] = values[0]
								if len(values) != 1 {
									result.err = fmt.Errorf("duplicate field %s", key)
								}
							}
							file, _, err := r.FormFile("image")
							if result.err != nil {
								observed <- result
								w.WriteHeader(http.StatusBadRequest)
								return
							}
							result.err = err
							if err == nil {
								data, err := io.ReadAll(file)
								result.image, result.err = string(data), err
								_ = file.Close()
							}
						}
					} else {
						data, err := io.ReadAll(r.Body)
						result.err = err
						if err == nil {
							result.err = common.Unmarshal(data, &result.fields)
						}
					}
					observed <- result
					w.Header().Set("Content-Type", "application/json")
					w.WriteHeader(http.StatusBadRequest)
					_, _ = io.WriteString(w, `{"error":{"message":"fixture rejection","type":"invalid_request_error"}}`)
				}))
				defer upstream.Close()
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, path, &body)
				c.Request.Header.Set("Content-Type", contentType)
				channelType := constant.ChannelTypeNewAPI
				if tc.ordinaryChannel {
					channelType = constant.ChannelTypeOpenAI
				}
				common.SetContextKey(c, constant.ContextKeyChannelType, channelType)
				common.SetContextKey(c, constant.ContextKeyOriginalModel, model)
				common.SetContextKey(c, constant.ContextKeyChannelBaseUrl, upstream.URL)
				common.SetContextKey(c, constant.ContextKeyChannelModelMapping, tc.mapping)
				common.SetContextKey(c, constant.ContextKeyChannelSetting, dto.ChannelSettings{PassThroughBodyEnabled: tc.passthrough})
				request, err := helper.GetAndValidOpenAIImageRequest(c, mode)
				require.NoError(t, err)
				info := &relaycommon.RelayInfo{Request: request, OriginModelName: model, RelayMode: mode, RelayFormat: types.RelayFormatOpenAIImage, RequestURLPath: path, UsingGroup: "default"}
				service.MarkMediaGenerationRequest(c, info)
				require.True(t, service.IsMediaGenerationRequest(c))
				require.Nil(t, relay.PrepareRequestBilling(c, info))
				assert.Equal(t, tc.size, request.Size, "billing keeps the original client size")
				assert.Equal(t, tc.quality, request.Quality, "billing keeps the original client quality")
				apiErr := relay.ImageHelper(c, info)
				require.NotNil(t, apiErr)
				require.Equal(t, "fixture rejection", apiErr.Error())
				result := <-observed
				require.NoError(t, result.err)
				for key, value := range map[string]string{"size": tc.wantSize, "quality": tc.wantQuality} {
					if value == "" {
						assert.NotContains(t, result.fields, key)
					} else {
						assert.Equal(t, value, result.fields[key])
					}
				}
				assert.Equal(t, "b64_json", result.fields["response_format"])
				if format == "edit multipart" {
					assert.Equal(t, strconv.Itoa(dto.MaxImageN), result.fields["n"])
					assert.Equal(t, "image fixture", result.image)
					assert.Equal(t, tc.size, c.Request.MultipartForm.Value["size"][0], "client form must remain reusable")
				} else {
					assert.Equal(t, float64(dto.MaxImageN), result.fields["n"])
					if tc.passthrough {
						assert.Equal(t, true, result.fields["vendor_extension"])
					}
				}
				assert.True(t, service.IsMediaGenerationRequest(c))
				assert.False(t, service.ShouldRetryRelayError(c, types.NewErrorWithStatusCode(fmt.Errorf("upstream failed"), types.ErrorCodeBadResponse, http.StatusInternalServerError), 3))
				assert.Equal(t, dto.MaxImageN, info.ImageRequestCount)
				assert.Equal(t, tc.size, request.Size)
				assert.Equal(t, tc.quality, request.Quality)
			})
		}
	}
}

func TestGeminiImageCompatibilityPreservesOriginalBillingParameters(t *testing.T) {
	settings := config.GlobalConfig.Get("billing_setting").(*billing_setting.BillingSetting)
	previousModes, previousExpressions := settings.BillingMode, settings.BillingExpr
	settings.BillingMode = map[string]string{"gemini-nano-banana-2.1": billing_setting.BillingModeTieredExpr}
	settings.BillingExpr = map[string]string{"gemini-nano-banana-2.1": `param("size") == "3840x2160" && param("quality") == "high" && param("vendor_extension") == true ? tier("original", fixed(0.1)) : tier("fallback", fixed(0.01))`}
	previousGroups := ratio_setting.GroupRatio2JSONString()
	groups := ratio_setting.GetGroupRatioCopy()
	groups["image-compat-test"] = 0
	encoded, err := common.Marshal(groups)
	require.NoError(t, err)
	require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(string(encoded)))
	quotaSettings := operation_setting.GetQuotaSetting()
	previousFreePreconsume := quotaSettings.EnableFreeModelPreConsume
	quotaSettings.EnableFreeModelPreConsume = false
	t.Cleanup(func() {
		settings.BillingMode, settings.BillingExpr = previousModes, previousExpressions
		quotaSettings.EnableFreeModelPreConsume = previousFreePreconsume
		require.NoError(t, ratio_setting.UpdateGroupRatioByJSONString(previousGroups))
	})
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", strings.NewReader(`{"model":"gemini-nano-banana-2.1","size":"3840x2160","quality":"high","n":2,"vendor_extension":true}`))
	c.Request.Header.Set("Content-Type", "application/json")
	common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeNewAPI)
	request, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesEdits)
	require.NoError(t, err)
	info := &relaycommon.RelayInfo{Request: request, OriginModelName: request.Model, RelayFormat: types.RelayFormatOpenAIImage, UsingGroup: "image-compat-test"}
	require.Nil(t, relay.PrepareRequestBilling(c, info))
	require.NotNil(t, info.TieredBillingSnapshot)
	assert.Equal(t, "original", info.TieredBillingSnapshot.EstimatedTier)
	assert.InDelta(t, 0.1*common.QuotaPerUnit, info.TieredBillingSnapshot.EstimatedQuotaBeforeGroup, 0.001)
}

func TestGeminiImageCompatibilityRejectsInvalidCount(t *testing.T) {
	for _, n := range []string{"-1", "129", "1.5", "18446744073686646784"} {
		for _, format := range []string{"JSON", "multipart"} {
			t.Run(format+"/"+n, func(t *testing.T) {
				var body bytes.Buffer
				contentType := "application/json"
				if format == "multipart" {
					writer := multipart.NewWriter(&body)
					for key, value := range map[string]string{"model": "gemini-nano-banana-2.1", "size": "3840x2160", "quality": "high", "n": n} {
						require.NoError(t, writer.WriteField(key, value))
					}
					require.NoError(t, writer.Close())
					contentType = writer.FormDataContentType()
				} else {
					_, err := fmt.Fprintf(&body, `{"model":"gemini-nano-banana-2.1","size":"3840x2160","quality":"high","n":%s}`, n)
					require.NoError(t, err)
				}
				c, _ := gin.CreateTestContext(httptest.NewRecorder())
				c.Request = httptest.NewRequest(http.MethodPost, "/v1/images/edits", &body)
				c.Request.Header.Set("Content-Type", contentType)
				common.SetContextKey(c, constant.ContextKeyChannelType, constant.ChannelTypeNewAPI)
				_, err := helper.GetAndValidOpenAIImageRequest(c, relayconstant.RelayModeImagesEdits)
				require.Error(t, err)
			})
		}
	}
}

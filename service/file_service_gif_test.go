package service

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/gif"
	"mime"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestGIFFileSourcesPreserveAnimationAndMIME(t *testing.T) {
	palette := color.Palette{color.Black, color.White}
	first := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second := image.NewPaletted(image.Rect(0, 0, 1, 1), palette)
	second.Pix[0] = 1
	var animation bytes.Buffer
	require.NoError(t, gif.EncodeAll(&animation, &gif.GIF{Image: []*image.Paletted{first, second}, Delay: []int{20, 20}, LoopCount: 0}))
	encoded := base64.StdEncoding.EncodeToString(animation.Bytes())
	savedConfig := common.GetDiskCacheConfig()
	savedFetch := *system_setting.GetFetchSetting()
	savedClient, savedProtectedClient := httpClient, ssrfProtectedHTTPClient
	savedWorker := system_setting.WorkerUrl
	savedDownloadLimit := constant.MaxFileDownloadMB
	t.Cleanup(func() {
		common.SetDiskCacheConfig(savedConfig)
		*system_setting.GetFetchSetting() = savedFetch
		httpClient, ssrfProtectedHTTPClient = savedClient, savedProtectedClient
		system_setting.WorkerUrl = savedWorker
		constant.MaxFileDownloadMB = savedDownloadLimit
	})
	// All downloads stay on this local fixture server.
	httpClient = &http.Client{}
	system_setting.GetFetchSetting().EnableSSRFProtection = false
	system_setting.WorkerUrl = ""
	constant.MaxFileDownloadMB = 1
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Deliberately stale PNG metadata must not relabel GIF bytes.
		w.Header().Set("Content-Type", "image/png")
		_, _ = w.Write(animation.Bytes())
	}))
	defer server.Close()
	for _, disk := range []bool{false, true} {
		common.SetDiskCacheConfig(common.DiskCacheConfig{Enabled: disk, ThresholdMB: 0, MaxSizeMB: 10, Path: t.TempDir()})
		for _, source := range []types.FileSource{
			types.NewBase64FileSource(encoded, ""),
			types.NewBase64FileSource("data:image/gif;base64,"+encoded, ""),
			types.NewBase64FileSource(encoded, "image/png"),
			types.NewURLFileSource(server.URL + "/animation.png"),
		} {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			cached, err := LoadFileSource(c, source)
			require.NoError(t, err)
			assert.Equal(t, disk, cached.IsDisk())
			assert.Equal(t, "image/gif", cached.MimeType)
			assert.Equal(t, "gif", cached.ImageFormat)
			require.NotNil(t, cached.ImageConfig)
			assert.Equal(t, 1, cached.ImageConfig.Width)
			got, err := cached.GetBase64Data()
			require.NoError(t, err)
			assert.Equal(t, encoded, got, "cache must retain every frame byte")
			decoded, err := base64.StdEncoding.DecodeString(got)
			require.NoError(t, err)
			gifData, err := gif.DecodeAll(bytes.NewReader(decoded))
			require.NoError(t, err)
			assert.Len(t, gifData.Image, 2)
			assert.Equal(t, []int{20, 20}, gifData.Delay)
			again, err := LoadFileSource(c, source)
			require.NoError(t, err)
			assert.Same(t, cached, again)
			CleanupFileSources(c)
		}
	}
	assert.Equal(t, "image/gif", GetMimeTypeByExtension("GIF"))
	extensions, err := mime.ExtensionsByType("image/gif")
	require.NoError(t, err)
	assert.Contains(t, extensions, ".gif")
	// A generic binary download with no extension also resolves to GIF.
	assert.Equal(t, "image/gif", smartDetectMimeType(&http.Response{Header: http.Header{"Content-Type": {"application/octet-stream"}}}, server.URL, animation.Bytes()))
}

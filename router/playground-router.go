package router

import (
	"strings"

	"github.com/QuantumNous/new-api/controller"
	"github.com/QuantumNous/new-api/middleware"
	pluginruntime "github.com/QuantumNous/new-api/pkg/jsplugin"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

// SetPlaygroundMediaRouter exposes the host media protocols to dashboard
// sessions using the same relay, task ownership and billing handlers as /v1.
func SetPlaygroundMediaRouter(router *gin.Engine) {
	playground := router.Group("/pg")
	playground.Use(middleware.DisableCache(), middleware.UserAuth())
	for _, protocol := range pluginruntime.HostProtocols() {
		switch protocol.Name {
		case "openai_image", "openai_video", "openai_responses":
		default:
			continue
		}
		for _, operation := range protocol.Operations {
			for _, method := range operation.Methods {
				handlers, err := taskPluginProtocolHandlers(protocol.Name, operation.Name, middleware.PreparePlaygroundRelay())
				if err != nil {
					panic(err)
				}
				playground.Handle(method, strings.TrimPrefix(operation.Path, "/v1"), handlers...)
			}
		}
	}
	playground.POST("/audio/speech",
		middleware.RouteTag("relay"), middleware.SystemPerformanceCheck(), middleware.PreparePlaygroundRelay(),
		middleware.ModelRequestRateLimit(), middleware.Distribute(),
		func(c *gin.Context) { controller.Relay(c, types.RelayFormatOpenAIAudio) },
	)
}

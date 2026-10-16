package middleware

import (
	"fmt"
	"net/http"
	"strings"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/QuantumNous/new-api/setting/ratio_setting"
	"github.com/gin-gonic/gin"
)

// PreparePlaygroundRelay runs after UserAuth. Session access tokens remain
// mandatory; refresh cookies and dashboard PATs never authorize model calls.
func PreparePlaygroundRelay() gin.HandlerFunc {
	return func(c *gin.Context) {
		if _, ok := GetSessionAuthIdentity(c); !ok {
			abortWithOpenAiMessage(c, http.StatusForbidden, i18n.T(c, i18n.MsgRelayAccessTokenUnsupported))
			return
		}

		group := c.Query("group")
		if c.Request.Method == http.MethodPost {
			request, err := getModelFromRequest(c)
			if err != nil {
				status := http.StatusBadRequest
				if common.IsRequestBodyTooLargeError(err) {
					status = http.StatusRequestEntityTooLarge
				}
				abortWithOpenAiMessage(c, status, i18n.T(c, i18n.MsgDistributorInvalidRequest, map[string]any{"Error": err.Error()}))
				return
			}
			group = request.Group
		}

		userGroup := common.GetContextKeyString(c, constant.ContextKeyUserGroup)
		if group == "" {
			group = userGroup
		}
		if !service.GroupInUserUsableGroups(userGroup, group) {
			abortWithOpenAiMessage(c, http.StatusForbidden, i18n.T(c, i18n.MsgDistributorGroupAccessDenied))
			return
		}
		if group != "auto" && !ratio_setting.ContainsGroupRatio(group) {
			abortWithOpenAiMessage(c, http.StatusForbidden, i18n.T(c, i18n.MsgAuthTokenGroupDeprecated, map[string]any{"Group": group}))
			return
		}

		common.SetContextKey(c, constant.ContextKeyUsingGroup, group)
		common.SetContextKey(c, constant.ContextKeyIsPlayground, true)
		if err := SetupContextForToken(c, &model.Token{
			UserId: c.GetInt("id"),
			Name:   fmt.Sprintf("playground-%s", group),
			Group:  group,
		}); err != nil {
			return
		}

		// Protocol selection, task presentation and upstream adaptors all use
		// the canonical /v1 path. Keep playground billing explicit across copies
		// of the request, and restore the browser path for outer middleware.
		originalURL, originalURI := c.Request.URL, c.Request.RequestURI
		relayURL := *originalURL
		relayURL.Path = "/v1" + strings.TrimPrefix(relayURL.Path, "/pg")
		if relayURL.RawPath != "" {
			relayURL.RawPath = "/v1" + strings.TrimPrefix(relayURL.RawPath, "/pg")
		}
		c.Request.URL = &relayURL
		c.Request.RequestURI = relayURL.RequestURI()
		defer func() {
			c.Request.URL = originalURL
			c.Request.RequestURI = originalURI
		}()
		c.Next()
	}
}

package controller

import (
	"encoding/json"
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetVideoModelCapabilities(c *gin.Context) {
	snapshot, err := model.GetVideoModelCapabilities(c.Query("model"))
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": err.Error()})
		return
	}
	common.ApiSuccess(c, snapshot)
}

func UpdateVideoModelCapabilities(c *gin.Context) {
	var request struct {
		ModelName         string          `json:"model_name"`
		VideoCapabilities json.RawMessage `json:"video_capabilities"`
		ExpectedVersion   string          `json:"expected_version"`
	}
	if err := common.DecodeJson(c.Request.Body, &request); err != nil || len(request.VideoCapabilities) == 0 {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid parameters"})
		return
	}
	var value *model.VideoModelCapabilities
	if err := common.Unmarshal(request.VideoCapabilities, &value); err != nil {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid parameters"})
		return
	}
	snapshot, err := model.UpdateVideoModelCapabilities(request.ModelName, value, request.ExpectedVersion)
	if err != nil {
		status := http.StatusBadRequest
		if errors.Is(err, model.ErrVideoModelCapabilitiesConflict) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"success": false, "message": err.Error()})
		return
	}
	recordManageAudit(c, "model.video_capabilities.update", map[string]any{"model_name": request.ModelName, "reset": value == nil})
	common.ApiSuccess(c, snapshot)
}

package controller

import (
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/gin-gonic/gin"
)

func GetRequestPolicy(c *gin.Context) {
	c.JSON(http.StatusOK, gin.H{"success": true, "data": gin.H{"options": model.CurrentRequestPolicy().Options}})
}

func UpdateRequestPolicy(c *gin.Context) {
	var request struct {
		Options map[string]string `json:"options"`
	}
	if err := c.ShouldBindJSON(&request); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	if err := model.UpdateRequestPolicyOptions(request.Options); err != nil {
		common.ApiErrorStatus(c, http.StatusBadRequest, err)
		return
	}
	GetRequestPolicy(c)
}

package controller

import (
	"errors"
	"net/http"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func GetUserRankings(c *gin.Context) {
	period := c.DefaultQuery("period", "today")
	switch period {
	case "today", "week", "month", "year":
	default:
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Invalid ranking period"})
		return
	}
	result, err := service.GetUserRankings(c.Request.Context(), period, c.GetInt("id"))
	if err != nil {
		common.SysError("user rankings query failed: " + err.Error())
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Rankings are temporarily unavailable"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": result})
}

func GetRankingParticipation(c *gin.Context) {
	preference, err := model.GetUserRankingPreference(c.Request.Context(), c.GetInt("id"))
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Could not load ranking preference"})
		return
	}
	c.JSON(http.StatusOK, gin.H{"success": true, "data": preference})
}

func UpdateRankingParticipation(c *gin.Context) {
	var request struct {
		Participating *bool   `json:"participating"`
		Nickname      *string `json:"nickname"`
	}
	if err := c.ShouldBindJSON(&request); err != nil || (request.Participating == nil) == (request.Nickname == nil) {
		c.JSON(http.StatusBadRequest, gin.H{"success": false, "message": "Provide either a participation choice or a nickname"})
		return
	}
	var err error
	if request.Nickname != nil {
		err = model.SetRankingNickname(c.Request.Context(), c.GetInt("id"), *request.Nickname, time.Now())
	} else {
		err = model.SetUserRankingPreference(c.Request.Context(), c.GetInt("id"), *request.Participating)
	}
	if errors.Is(err, model.ErrRankingNicknameCooldown) || errors.Is(err, model.ErrInvalidRankingNickname) {
		status := http.StatusBadRequest
		if errors.Is(err, model.ErrRankingNicknameCooldown) {
			status = http.StatusConflict
		}
		c.JSON(status, gin.H{"success": false, "message": err.Error()})
		return
	}
	if err != nil {
		c.JSON(http.StatusServiceUnavailable, gin.H{"success": false, "message": "Could not save ranking preference"})
		return
	}
	service.InvalidateUserRankings()
	GetRankingParticipation(c)
}

package controller

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
)

func GetRankings(c *gin.Context) {
	result, err := service.RequestRankingsSnapshot(c.Request.Context(), c.DefaultQuery("period", "week"))
	if errors.Is(err, service.ErrRankingsPending) {
		c.Header("Retry-After", "1")
		c.JSON(http.StatusAccepted, gin.H{"success": true, "pending": true})
		return
	}
	if err != nil {
		c.JSON(http.StatusBadRequest, gin.H{
			"success": false,
			"message": err.Error(),
		})
		return
	}

	c.JSON(http.StatusOK, gin.H{
		"success": true,
		"data":    result,
	})
}

package service

import (
	"errors"
	"net/http"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/logger"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/gin-gonic/gin"
)

var errChannelConcurrencyLimited = errors.New("channel concurrency limit reached")
var errChannelConcurrencyUnavailable = errors.New("channel concurrency service unavailable")

func acquireChannelConcurrency(c *gin.Context, channel *model.Channel) error {
	acquired, err := TryAcquireChannelConcurrency(c, channel)
	if err != nil {
		logger.LogError(c, common.LogText("channel concurrency acquisition failed: %s", err.Error()))
		return errChannelConcurrencyUnavailable
	}
	if !acquired {
		return errChannelConcurrencyLimited
	}
	return nil
}

// AcquireChannelConcurrency also covers pinned channels and reused WebSocket
// connections, which do not go through weighted channel selection.
func AcquireChannelConcurrency(c *gin.Context, channel *model.Channel) *types.NewAPIError {
	return ChannelConcurrencyError(c, acquireChannelConcurrency(c, channel))
}

func channelConcurrencySelectionError(err error) *ChannelSelectError {
	switch {
	case errors.Is(err, errChannelConcurrencyLimited):
		return &ChannelSelectError{StatusCode: http.StatusTooManyRequests, Code: "channel_concurrency_limit", MessageID: i18n.MsgChannelConcurrencyLimited}
	case errors.Is(err, errChannelConcurrencyUnavailable):
		return &ChannelSelectError{StatusCode: http.StatusServiceUnavailable, Code: "channel_concurrency_unavailable", MessageID: i18n.MsgChannelConcurrencyUnavailable}
	default:
		return nil
	}
}

// ChannelConcurrencyError preserves capacity errors across HTTP retry and
// WebSocket paths without treating a busy channel as an upstream failure.
func ChannelConcurrencyError(c *gin.Context, err error) *types.NewAPIError {
	selectionErr := channelConcurrencySelectionError(err)
	if selectionErr == nil {
		return nil
	}
	return types.NewErrorWithStatusCode(errors.New(i18n.T(c, selectionErr.MessageID)), selectionErr.Code, selectionErr.StatusCode, types.ErrOptionWithSkipRetry())
}

func selectChannelWithConcurrency(c *gin.Context, group, modelName string, retry int, filters []dto.ChannelFilter) (*model.Channel, error) {
	var fullChannels []int
	for {
		channel, err := model.GetRandomSatisfiedChannel(group, modelName, retry, filters, fullChannels...)
		if err != nil {
			return nil, err
		}
		if channel == nil {
			if len(fullChannels) > 0 {
				return nil, errChannelConcurrencyLimited
			}
			return nil, nil
		}
		if err := acquireChannelConcurrency(c, channel); err != nil {
			if errors.Is(err, errChannelConcurrencyLimited) {
				fullChannels = append(fullChannels, channel.Id)
				continue
			}
			return nil, err
		}
		return channel, nil
	}
}

package service

import (
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestRecentChannelProbeAverageTTFT(t *testing.T) {
	for _, tc := range []struct {
		name    string
		history []model.ChannelProbeResult
		want    *float64
	}{
		{name: "no requests"},
		{
			name: "fewer than five keeps fractional mean",
			history: []model.ChannelProbeResult{
				{Success: true, TTFTMS: common.GetPointer(int64(100))},
				{Success: true, TTFTMS: common.GetPointer(int64(201))},
			},
			want: common.GetPointer(150.5),
		},
		{
			name: "zero is a valid measurement",
			history: []model.ChannelProbeResult{
				{Success: true, TTFTMS: common.GetPointer(int64(0))},
			},
			want: common.GetPointer(0.0),
		},
		{
			name: "first five in query order regardless of window age",
			history: []model.ChannelProbeResult{
				{CheckedAt: 10000, TTFTMS: common.GetPointer(int64(100))},
				{CheckedAt: 6400, TTFTMS: common.GetPointer(int64(200))},
				{CheckedAt: 2800, TTFTMS: common.GetPointer(int64(300))},
				{CheckedAt: 1000, TTFTMS: common.GetPointer(int64(400))},
				{CheckedAt: 1000, TTFTMS: common.GetPointer(int64(500))},
				{CheckedAt: 1000, TTFTMS: common.GetPointer(int64(9000))},
			},
			want: common.GetPointer(300.0),
		},
		{
			name: "missing and negative measurements consume slots without diluting mean",
			history: []model.ChannelProbeResult{
				{Success: false, ErrorCode: "timeout"},
				{Success: true},
				{TTFTMS: common.GetPointer(int64(-1))},
				{Success: true, TTFTMS: common.GetPointer(int64(100))},
				{Success: true, TTFTMS: common.GetPointer(int64(201))},
				{Success: true, TTFTMS: common.GetPointer(int64(9000))},
			},
			want: common.GetPointer(150.5),
		},
		{
			name: "no valid measurement in first five does not use sixth",
			history: []model.ChannelProbeResult{
				{}, {}, {}, {}, {},
				{Success: true, TTFTMS: common.GetPointer(int64(100))},
			},
		},
		{
			name: "failure after first token retains measured TTFT",
			history: []model.ChannelProbeResult{
				{Success: false, TTFTMS: common.GetPointer(int64(200))},
			},
			want: common.GetPointer(200.0),
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := recentChannelProbeAverageTTFT(tc.history)
			if tc.want == nil {
				assert.Nil(t, got)
				return
			}
			require.NotNil(t, got)
			assert.Equal(t, *tc.want, *got)
		})
	}
}

func TestPublicChannelProbeRecentAverageTTFT(t *testing.T) {
	view := ChannelProbeView{
		History: []model.ChannelProbeResult{
			{TTFTMS: common.GetPointer(int64(100))},
			{TTFTMS: common.GetPointer(int64(201))},
		},
	}
	public := PublicChannelProbe(view)
	require.NotNil(t, public.RecentAverageTTFTMS)
	assert.Equal(t, 150.5, *public.RecentAverageTTFTMS)

	// A model's mean is already calculated from its raw history; the display
	// history on the parent view may contain fewer samples after windowing.
	view.History = nil
	view.ModelStatuses = []PublicChannelProbeView{{RecentAverageTTFTMS: common.GetPointer(250.5)}}
	public = PublicChannelProbe(view)
	require.NotNil(t, public.RecentAverageTTFTMS)
	assert.Equal(t, 250.5, *public.RecentAverageTTFTMS)
}

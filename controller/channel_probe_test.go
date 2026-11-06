package controller

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	"github.com/QuantumNous/new-api/i18n"
	"github.com/QuantumNous/new-api/middleware"
	"github.com/QuantumNous/new-api/model"
	relaycommon "github.com/QuantumNous/new-api/relay/common"
	"github.com/QuantumNous/new-api/relaykit/types"
	"github.com/QuantumNous/new-api/service"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func channelProbeTestDB(t *testing.T) *gorm.DB {
	t.Helper()
	oldDB, oldType := model.DB, common.MainDatabaseType()
	var dialector gorm.Dialector = sqlite.Open(":memory:")
	dbType := common.DatabaseTypeSQLite
	if dsn := os.Getenv("CHANNEL_PROBE_TEST_MYSQL_DSN"); dsn != "" {
		dialector, dbType = mysql.Open(dsn), common.DatabaseTypeMySQL
	} else if dsn := os.Getenv("CHANNEL_PROBE_TEST_POSTGRES_DSN"); dsn != "" {
		dialector, dbType = postgres.New(postgres.Config{DSN: dsn, PreferSimpleProtocol: true}), common.DatabaseTypePostgreSQL
	}
	db, err := gorm.Open(dialector, &gorm.Config{})
	require.NoError(t, err)
	versionQuery := "SELECT version()"
	if dbType == common.DatabaseTypeSQLite {
		versionQuery = "SELECT sqlite_version()"
	}
	var version string
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("probe database: %s %s", dbType, version)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	model.DB = db
	common.SetMainDatabaseType(dbType)
	t.Cleanup(func() {
		// Only the dedicated test database may be supplied through the DSNs.
		_ = db.Migrator().DropTable(&model.ChannelProbeMinute{}, &model.ChannelProbeResult{}, &model.ChannelProbe{}, &model.Ability{}, &model.Channel{})
		model.DB = oldDB
		common.SetMainDatabaseType(oldType)
		if oldDB != nil {
			_ = service.RefreshChannelProbeCache()
		}
		_ = sqlDB.Close()
	})
	require.NoError(t, model.MigrateChannelProbes(db))
	return db
}

func channelProbeFixture() model.ChannelProbe {
	return model.ChannelProbe{ChannelID: 123, Model: "probe-model", PublicName: "Public display", Enabled: true, Public: true, ActiveEnabled: true, PassiveEnabled: true, IntervalSeconds: 60, TimeoutSeconds: 5, GreenMS: 1000, YellowMS: 3000}
}

func TestChannelProbeContentTiming(t *testing.T) {
	for _, tc := range []struct {
		name, payload    string
		content, success bool
	}{
		{"role and usage", "data: {\"choices\":[{\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: {\"usage\":{\"completion_tokens\":1}}\n\ndata: [DONE]\n\n", false, true},
		{"OpenAI text", "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n", true, true},
		{"OpenAI reasoning", "data: {\"choices\":[{\"delta\":{\"reasoning_content\":\"think\"}}]}\n\n", true, true},
		{"OpenAI tools", "data: {\"choices\":[{\"delta\":{\"tool_calls\":[{\"function\":{\"arguments\":\"{}\"}}]}}]}\n\n", true, true},
		{"Claude thinking", "data: {\"type\":\"content_block_delta\",\"delta\":{\"thinking\":\"hi\"}}\n\n", true, true},
		{"Responses metadata", "data: {\"type\":\"response.created\",\"response\":{\"id\":\"resp\"}}\n\n", false, true},
		{"Responses text", "data: {\"type\":\"response.output_text.delta\",\"delta\":\"hi\"}\n\n", true, true},
		{"Gemini text", "data: {\"candidates\":[{\"content\":{\"parts\":[{\"text\":\"hi\"}]}}]}\n\n", true, true},
		{"late error", "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\ndata: {\"error\":{\"message\":\"TOKEN upstream-url\"}}\n\n", true, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c, _ := gin.CreateTestContext(httptest.NewRecorder())
			observer := service.NewChannelProbeObserver(c.Writer)
			observer.Header().Set("Content-Type", "text/event-stream")
			// Network writes need not align with SSE lines or JSON tokens.
			_, err := observer.Write([]byte(tc.payload[:9]))
			require.NoError(t, err)
			_, err = observer.WriteString(tc.payload[9:])
			require.NoError(t, err)
			result := observer.Result(context.Background(), nil, nil)
			assert.Equal(t, tc.content, result.TTFTMS != nil)
			assert.Equal(t, tc.success, result.Success)
			data, err := common.Marshal(result)
			require.NoError(t, err)
			assert.NotContains(t, string(data), "TOKEN")
			assert.NotContains(t, string(data), "upstream-url")
		})
	}
	probe := channelProbeFixture()
	for _, tc := range []struct {
		ms     int64
		status string
	}{{999, "green"}, {1000, "yellow"}, {2999, "yellow"}, {3000, "red"}} {
		assert.Equal(t, tc.status, service.ChannelProbeStatus(true, &tc.ms, probe))
	}
	assert.Equal(t, "unknown", service.ChannelProbeStatus(true, nil, probe))
	assert.Equal(t, "red", service.ChannelProbeStatus(false, nil, probe))
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	observer := service.NewChannelProbeObserver(c.Writer)
	observer.Header().Set("Content-Type", "application/json")
	_, err := observer.WriteString(`{"choices":[{"message":{"content":"hello"}}]}`)
	require.NoError(t, err)
	passive := observer.Result(context.Background(), &relaycommon.RelayInfo{IsStream: false}, nil)
	assert.True(t, passive.Success)
	assert.Nil(t, passive.TTFTMS)
	assert.Equal(t, "unknown", service.ChannelProbeStatus(passive.Success, passive.TTFTMS, probe))
}

func TestChannelProbePersistenceAndAttemptIsolation(t *testing.T) {
	db := channelProbeTestDB(t)
	first := channelProbeFixture()
	require.NoError(t, model.SaveChannelProbe(&first, true))
	duplicate := first
	assert.Error(t, model.SaveChannelProbe(&duplicate, true), "the same target/model set must not schedule duplicate traffic")
	second := first
	second.ID, second.ChannelID = 0, 124
	require.NoError(t, model.SaveChannelProbe(&second, true))
	// Simulate upgrade/restart twice, with existing configuration preserved.
	require.NoError(t, model.MigrateChannelProbes(db))
	require.NoError(t, model.MigrateChannelProbes(db))
	stored, err := model.GetChannelProbe(first.ID)
	require.NoError(t, err)
	assert.Equal(t, first.Model, stored.Model)

	claimed, err := model.ClaimChannelProbe(&first, false)
	require.NoError(t, err)
	require.True(t, claimed)
	contender := stored
	claimed, err = model.ClaimChannelProbe(&contender, true)
	require.NoError(t, err)
	assert.False(t, claimed)
	require.NoError(t, model.ReleaseChannelProbe(contender))
	stored, err = model.GetChannelProbe(first.ID)
	require.NoError(t, err)
	assert.Positive(t, stored.LeaseUntil, "another runner cannot release the lease")
	require.NoError(t, model.ReleaseChannelProbe(first))

	group := channelProbeFixture()
	group.Scope, group.GroupName, group.ChannelID = "group", "fixture-group", 0
	require.NoError(t, model.SaveChannelProbe(&group, true))
	require.NoError(t, service.RefreshChannelProbeCache())
	c, _ := gin.CreateTestContext(httptest.NewRecorder())
	c.Request = httptest.NewRequest(http.MethodPost, "/v1/chat/completions", nil)
	info := &relaycommon.RelayInfo{OriginModelName: first.Model, TokenGroup: "fixture-group", UsingGroup: "fixture-group", IsStream: true}
	originalWriter := c.Writer
	common.SetContextKey(c, constant.ContextKeyRequestStartTime, time.Now().Add(-200*time.Millisecond))
	finishGroup := service.BeginPassiveGroupProbe(c, info)
	groupWriter := c.Writer
	finish := service.BeginPassiveChannelProbe(c, info, first.ChannelID)
	finish(types.NewError(errors.New("private upstream TOKEN"), types.ErrorCodeDoRequestFailed))
	assert.Same(t, groupWriter, c.Writer)
	finish = service.BeginPassiveChannelProbe(c, info, second.ChannelID)
	c.Writer.Header().Set("Content-Type", "text/event-stream")
	_, err = c.Writer.WriteString("data: {\"choices\":[{\"delta\":{\"content\":\"hello\"}}]}\n\n")
	require.NoError(t, err)
	finish(nil)
	finishGroup(nil)
	assert.Same(t, originalWriter, c.Writer)
	groupResults, err := model.GetChannelProbeHistory(group.ID)
	require.NoError(t, err)
	require.Len(t, groupResults, 1)
	assert.True(t, groupResults[0].Success, "a failed upstream attempt must not mark the final successful group request unavailable")
	require.NotNil(t, groupResults[0].TTFTMS)
	assert.GreaterOrEqual(t, *groupResults[0].TTFTMS, int64(200), "group first-token latency starts with the original request")

	// No eligible initial channel never enters Relay: it still counts once.
	service.MarkChannelProbeRequestStart(c)
	service.RecordGroupProbeSelectionFailure(c, "fixture-group", first.Model)
	groupResults, err = model.GetChannelProbeHistory(group.ID)
	require.NoError(t, err)
	require.Len(t, groupResults, 2)
	assert.Equal(t, "channel_missing", groupResults[0].ErrorCode)
	// Auto-group requests belong to their final concrete group only.
	otherGroup := group
	otherGroup.GroupName = "fallback-group"
	require.NoError(t, model.SaveChannelProbe(&otherGroup, true))
	require.NoError(t, service.RefreshChannelProbeCache())
	info.TokenGroup, info.UsingGroup = "auto", "fixture-group"
	finishGroup = service.BeginPassiveGroupProbe(c, info)
	info.UsingGroup = "fallback-group"
	finishGroup(nil)
	groupResults, err = model.GetChannelProbeHistory(group.ID)
	require.NoError(t, err)
	assert.Len(t, groupResults, 2)
	finalGroupResults, err := model.GetChannelProbeHistory(otherGroup.ID)
	require.NoError(t, err)
	require.Len(t, finalGroupResults, 1)
	assert.True(t, finalGroupResults[0].Success)
	// WebSocket frames use the same final-group snapshot without HTTP writes.
	wsObserver := service.NewChannelProbeObserverAt(nil, time.Now().Add(-300*time.Millisecond))
	wsObserver.ObserveEvent([]byte(`{"type":"response.output_text.delta","delta":"hello"}`))
	service.RecordPassiveGroupProbeResult(service.PassiveGroupProbes("", first.Model), "fallback-group", wsObserver.Result(c.Request.Context(), info, nil))
	finalGroupResults, err = model.GetChannelProbeHistory(otherGroup.ID)
	require.NoError(t, err)
	require.Len(t, finalGroupResults, 2)
	require.NotNil(t, finalGroupResults[0].TTFTMS)
	assert.GreaterOrEqual(t, *finalGroupResults[0].TTFTMS, int64(300))
	failed, err := model.GetChannelProbeHistory(first.ID)
	require.NoError(t, err)
	require.Len(t, failed, 1)
	assert.False(t, failed[0].Success)
	assert.Nil(t, failed[0].TTFTMS)
	succeeded, err := model.GetChannelProbeHistory(second.ID)
	require.NoError(t, err)
	require.Len(t, succeeded, 1)
	assert.True(t, succeeded[0].Success)
	assert.NotNil(t, succeeded[0].TTFTMS)
	require.NoError(t, model.StoreChannelProbeResult(second, model.ChannelProbeResult{CheckedAt: succeeded[0].CheckedAt - 10, Source: "passive", Success: false}))
	ordered, err := model.GetChannelProbeHistory(second.ID)
	require.NoError(t, err)
	require.Len(t, ordered, 2)
	assert.Equal(t, succeeded[0].ID, ordered[0].ID, "late insertion of an older completion must not replace the latest sample")
	require.NoError(t, model.StoreChannelProbeResult(second, model.ChannelProbeResult{CheckedAt: succeeded[0].CheckedAt, Source: "passive", Success: false}))
	ordered, err = model.GetChannelProbeHistory(second.ID)
	require.NoError(t, err)
	require.Len(t, ordered, 3)
	assert.Greater(t, ordered[0].ID, succeeded[0].ID, "same-second completions use descending ID as the tie-breaker")
	assert.Equal(t, succeeded[0].ID, ordered[1].ID)

	// Bulk existing history exercises retention without 60 repeated transactions.
	seed := make([]model.ChannelProbeResult, 60)
	for i := range seed {
		seed[i] = model.ChannelProbeResult{ProbeID: first.ID, Model: first.Model, CheckedAt: time.Now().Unix(), Source: "passive", Success: true}
	}
	require.NoError(t, db.Create(&seed).Error)
	require.NoError(t, model.StoreChannelProbeResult(first, model.ChannelProbeResult{CheckedAt: time.Now().Unix(), Source: "passive", Success: true}))
	var count int64
	require.NoError(t, db.Model(&model.ChannelProbeResult{}).Where("probe_id = ?", first.ID).Count(&count).Error)
	assert.EqualValues(t, 60, count)
	retained, err := model.GetChannelProbeHistory(first.ID)
	require.NoError(t, err)
	require.Len(t, retained, 60)
	require.NoError(t, model.StoreChannelProbeResult(first, model.ChannelProbeResult{CheckedAt: retained[59].CheckedAt - 10, Source: "passive", Success: false}))
	afterLateInsert, err := model.GetChannelProbeHistory(first.ID)
	require.NoError(t, err)
	assert.Equal(t, retained, afterLateInsert, "retention must remove the late old sample rather than a newer completion")
	require.NoError(t, db.Model(&model.ChannelProbeResult{}).Where("probe_id = ?", first.ID).Count(&count).Error)
	assert.EqualValues(t, 60, count)
	view, err := service.BuildChannelProbeView(first, time.Now().Add(4*time.Minute))
	require.NoError(t, err)
	assert.True(t, view.Stale)
	assert.Equal(t, "unknown", view.Status)
	public, err := common.Marshal(service.PublicChannelProbe(view))
	require.NoError(t, err)
	for _, secret := range []string{"channel_id", "lease", "revision", "probe_id", "TOKEN", "\"id\""} {
		assert.NotContains(t, string(public), secret)
	}
	old := first
	first.Models = []string{"replacement"}
	first.Model = "replacement"
	require.NoError(t, model.SaveChannelProbe(&first, false))
	require.NoError(t, model.StoreChannelProbeResult(old, model.ChannelProbeResult{Source: "passive", Success: false}))
	history, err := model.GetChannelProbeHistory(first.ID)
	require.NoError(t, err)
	assert.Empty(t, history, "old in-flight result must not contaminate a newly configured model")
	require.NoError(t, model.DeleteChannelProbe(second.ID))
	history, err = model.GetChannelProbeHistory(second.ID)
	require.NoError(t, err)
	assert.Empty(t, history)
	require.NoError(t, model.StoreChannelProbeResult(first, model.ChannelProbeResult{Source: "passive", CheckedAt: time.Now().Add(-8 * 24 * time.Hour).Unix()}))
	require.NoError(t, model.CleanupChannelProbeResults())
	history, err = model.GetChannelProbeHistory(first.ID)
	require.NoError(t, err)
	assert.Empty(t, history, "observations older than seven days are removed")
}

func TestChannelProbeNativeAdapterWithoutUserOrBilling(t *testing.T) {
	db := channelProbeTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}))
	previousStreamingTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 5
	t.Cleanup(func() { constant.StreamingTimeout = previousStreamingTimeout })
	service.InitHttpClient()
	var requests atomic.Int32
	var responseMode atomic.Int32
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		assert.Equal(t, "/v1/chat/completions", r.URL.Path)
		assert.Equal(t, "Bearer fixture-key", r.Header.Get("Authorization"))
		w.Header().Set("Content-Type", "text/event-stream")
		switch responseMode.Load() {
		case 1:
			_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"role\":\"assistant\"}}]}\n\ndata: [DONE]\n\n"))
			return
		case 2:
			_, _ = w.Write([]byte("data: {\"choices\":[],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":0,\"total_tokens\":1}}\n\ndata: [DONE]\n\n"))
			return
		case 3:
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"hello"}}],"usage":{"prompt_tokens":1,"completion_tokens":1,"total_tokens":2}}`))
			return
		}
		_, _ = w.Write([]byte("data: {\"id\":\"test\",\"object\":\"chat.completion.chunk\",\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}],\"usage\":{\"prompt_tokens\":1,\"completion_tokens\":1,\"total_tokens\":2}}\n\ndata: [DONE]\n\n"))
	}))
	defer upstream.Close()
	url := upstream.URL
	channel := model.Channel{Id: 123, Type: constant.ChannelTypeOpenAI, Key: "fixture-key", BaseURL: &url, Name: "Internal secret channel", Models: "probe-model", Status: common.ChannelStatusEnabled}
	require.NoError(t, db.Create(&channel).Error)
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	result := RunActiveChannelProbe(ctx, channelProbeFixture())
	assert.True(t, result.Success, result.ErrorCode)
	assert.NotNil(t, result.TTFTMS)
	// This fixture has no users, tokens, or consume-log table. Success proves
	// scheduled probes bypass root identity, model pricing and consume logs.
	assert.False(t, db.Migrator().HasTable(&model.User{}))
	assert.False(t, db.Migrator().HasTable(&model.Log{}))
	expired, stop := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer stop()
	result = RunActiveChannelProbe(expired, channelProbeFixture())
	assert.False(t, result.Success)
	assert.Equal(t, "timeout", result.ErrorCode)
	assert.EqualValues(t, 1, requests.Load(), "expired probe must not make an upstream request")
	for _, tc := range []struct {
		name string
		mode int32
	}{{"role-only", 1}, {"usage-only", 2}, {"non-SSE 200", 3}} {
		t.Run(tc.name, func(t *testing.T) {
			responseMode.Store(tc.mode)
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			result := RunActiveChannelProbe(ctx, channelProbeFixture())
			assert.False(t, result.Success)
			assert.Equal(t, "no_content", result.ErrorCode)
			assert.Nil(t, result.TTFTMS)
		})
	}
}

// The old table shape deliberately retains the released unique index.
type legacyChannelProbe struct {
	ID                                                 int    `gorm:"primaryKey"`
	ChannelID                                          int    `gorm:"uniqueIndex:idx_channel_probe_model"`
	Model                                              string `gorm:"type:varchar(191);uniqueIndex:idx_channel_probe_model"`
	PublicName                                         string `gorm:"type:varchar(128)"`
	Enabled, Public, ActiveEnabled, PassiveEnabled     bool
	IntervalSeconds, TimeoutSeconds                    int
	GreenMS, YellowMS, Revision, NextRunAt, LeaseUntil int64
	LeaseToken                                         string `gorm:"type:varchar(36)"`
}

func (legacyChannelProbe) TableName() string { return "channel_probes" }

type legacyChannelProbeResult struct {
	ID        int64 `gorm:"primaryKey"`
	ProbeID   int
	CheckedAt int64
	Source    string `gorm:"type:varchar(16)"`
	Success   bool
	TTFTMS    *int64
	LatencyMS int64
	Status    string `gorm:"type:varchar(16)"`
	ErrorCode string `gorm:"type:varchar(32)"`
}

func (legacyChannelProbeResult) TableName() string { return "channel_probe_results" }

func TestChannelProbeUpgradeMultiModelWindowsAndOrder(t *testing.T) {
	db := channelProbeTestDB(t)
	require.NoError(t, db.Migrator().DropTable(&model.ChannelProbeMinute{}, &model.ChannelProbeResult{}, &model.ChannelProbe{}))
	require.NoError(t, db.AutoMigrate(&legacyChannelProbe{}, &legacyChannelProbeResult{}))
	now := time.Now().Truncate(time.Minute).Add(30 * time.Second)
	ms := int64(5000)
	legacy := legacyChannelProbe{ChannelID: 7, Model: "old-model", PublicName: "Legacy display", Enabled: true, Public: true, PassiveEnabled: true, IntervalSeconds: 60, TimeoutSeconds: 10, GreenMS: 5000, YellowMS: 10000}
	require.NoError(t, db.Create(&legacy).Error)
	require.NoError(t, db.Create(&legacyChannelProbeResult{ProbeID: legacy.ID, CheckedAt: now.Unix(), Source: "passive", Success: true, TTFTMS: &ms}).Error)
	require.NoError(t, model.MigrateChannelProbes(db))
	require.NoError(t, model.MigrateChannelProbes(db))
	assert.False(t, db.Migrator().HasIndex(&model.ChannelProbe{}, "idx_channel_probe_model"))
	upgraded, err := model.GetChannelProbe(legacy.ID)
	require.NoError(t, err)
	assert.Equal(t, "upstream", upgraded.Scope)
	assert.Equal(t, []string{"old-model"}, upgraded.Models)
	var nullSortOrders int64
	require.NoError(t, db.Model(&model.ChannelProbe{}).Where("sort_order IS NULL").Count(&nullSortOrders).Error)
	assert.Zero(t, nullSortOrders, "legacy default ordering must be identical across databases")
	oldView, err := service.BuildPublicChannelProbeModel(upgraded, upgraded.Model, now, 24)
	require.NoError(t, err)
	assert.EqualValues(t, 1, oldView.SampleCount, "second migration must not import samples again")
	assert.Equal(t, "yellow", oldView.Status, "exactly 5 seconds is yellow")
	first := channelProbeFixture()
	first.Scope, first.GroupName, first.ChannelID, first.Models = "group", "group-one", 0, []string{"model-a", "model-b"}
	first.GreenMS, first.YellowMS = 0, 0
	require.NoError(t, service.ValidateChannelProbe(&first))
	assert.EqualValues(t, 5000, first.GreenMS)
	assert.EqualValues(t, 10000, first.YellowMS)
	require.NoError(t, model.SaveChannelProbe(&first, true))
	duplicate := first
	duplicate.Models = []string{"model-b", "model-a"}
	assert.Error(t, model.SaveChannelProbe(&duplicate, true), "model order does not create a second target")
	second := first
	second.GroupName = "group-two"
	require.NoError(t, model.SaveChannelProbe(&second, true))
	require.NoError(t, model.OrderChannelProbes([]int{second.ID, first.ID, legacy.ID}))
	ordered, err := model.ListChannelProbeConfigs()
	require.NoError(t, err)
	assert.Equal(t, []int{second.ID, first.ID, legacy.ID}, []int{ordered[0].ID, ordered[1].ID, ordered[2].ID})
	assert.Error(t, model.OrderChannelProbes([]int{legacy.ID, legacy.ID, first.ID}))
	ordered, err = model.ListChannelProbeConfigs()
	require.NoError(t, err)
	assert.Equal(t, second.ID, ordered[0].ID, "invalid reorder rolls back atomically")
	for i := 0; i < 65; i++ {
		require.NoError(t, model.StoreChannelProbeResult(first, model.ChannelProbeResult{Model: "model-a", Source: "passive", CheckedAt: now.Add(-2 * time.Hour).Unix(), Success: true, TTFTMS: &ms}))
	}
	require.NoError(t, model.StoreChannelProbeResult(first, model.ChannelProbeResult{Model: "model-a", Source: "passive", CheckedAt: now.Unix(), ErrorCode: "timeout"}))
	require.NoError(t, model.StoreChannelProbeResult(first, model.ChannelProbeResult{Model: "model-a", Source: "passive", CheckedAt: now.Add(-23 * time.Hour).Unix(), Success: true, TTFTMS: &ms}))
	require.NoError(t, model.StoreChannelProbeResult(first, model.ChannelProbeResult{Model: "model-b", Source: "passive", CheckedAt: now.Unix(), Success: true, TTFTMS: &ms}))
	raw, err := model.GetChannelProbeHistory(first.ID, "model-a")
	require.NoError(t, err)
	assert.Len(t, raw, 60)
	for _, tc := range []struct {
		hours          int
		count, success int64
	}{{1, 1, 0}, {6, 66, 65}, {12, 66, 65}, {24, 67, 66}} {
		t.Run(fmt.Sprintf("window-%dh", tc.hours), func(t *testing.T) {
			view, err := service.BuildPublicChannelProbeModel(first, "model-a", now, tc.hours)
			require.NoError(t, err)
			assert.Equal(t, tc.count, view.SampleCount)
			assert.Equal(t, tc.success, view.SuccessCount)
			require.Len(t, view.Timeline, 60)
			assert.Equal(t, "timeout", view.Status)
			assert.Equal(t, "timeout", view.Timeline[59].Status)
			assert.EqualValues(t, tc.hours*3600, view.WindowEnd-view.WindowStart)
			var count, success int64
			for _, b := range view.Timeline {
				count += b.Count
				success += b.SuccessCount
				if b.Count == 0 {
					assert.Equal(t, "unknown", b.Status)
					assert.Nil(t, b.TTFTMS)
				}
			}
			assert.Equal(t, tc.count, count)
			assert.Equal(t, tc.success, success)
			encoded, err := common.Marshal(view)
			require.NoError(t, err)
			for _, secret := range []string{"group-one", "channel_id", "probe_id", "revision", "group_name"} {
				assert.NotContains(t, string(encoded), secret)
			}
		})
	}
	viewB, err := service.BuildPublicChannelProbeModel(first, "model-b", now, 24)
	require.NoError(t, err)
	assert.EqualValues(t, 1, viewB.SampleCount)
	assert.Equal(t, "yellow", viewB.Status)
	old := first
	first.GroupName = "renamed-group"
	require.NoError(t, model.SaveChannelProbe(&first, false))
	require.NoError(t, model.StoreChannelProbeResult(old, model.ChannelProbeResult{Model: "model-b", Source: "passive", CheckedAt: now.Unix(), Success: true}))
	after, err := service.BuildPublicChannelProbeModel(first, "model-b", now, 24)
	require.NoError(t, err)
	assert.Zero(t, after.SampleCount)
	assert.Nil(t, after.Latest)
}

func TestChannelProbeGroupRoutingAndModelTargets(t *testing.T) {
	db := channelProbeTestDB(t)
	require.NoError(t, db.AutoMigrate(&model.Channel{}, &model.Ability{}))
	oldRetry, oldCache := common.RetryTimes, common.MemoryCacheEnabled
	oldTimeout := constant.StreamingTimeout
	constant.StreamingTimeout = 5
	t.Cleanup(func() { constant.StreamingTimeout = oldTimeout })
	common.RetryTimes, common.MemoryCacheEnabled = 1, false
	t.Cleanup(func() { common.RetryTimes, common.MemoryCacheEnabled = oldRetry, oldCache })
	service.InitHttpClient()
	var failedCalls, successfulCalls atomic.Int32
	var models []string
	failing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		failedCalls.Add(1)
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(503)
		_, _ = w.Write([]byte(`{"error":{"message":"fixture unavailable","type":"server_error"}}`))
	}))
	defer failing.Close()
	success := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		successfulCalls.Add(1)
		var req struct {
			Model string `json:"model"`
		}
		require.NoError(t, common.DecodeJson(r.Body, &req))
		models = append(models, req.Model)
		w.Header().Set("Content-Type", "text/event-stream")
		_, _ = w.Write([]byte("data: {\"choices\":[{\"index\":0,\"delta\":{\"content\":\"hello\"}}]}\n\ndata: {\"choices\":[{\"index\":0,\"delta\":{},\"finish_reason\":\"stop\"}]}\n\ndata: [DONE]\n\n"))
	}))
	defer success.Close()
	high, low := int64(20), int64(10)
	urlA, urlB := failing.URL, success.URL
	channels := []model.Channel{{Id: 1, Type: constant.ChannelTypeOpenAI, Key: "fixture-a", BaseURL: &urlA, Models: "model-a,model-b", Group: "fixture-group", Status: common.ChannelStatusEnabled, Priority: &high}, {Id: 2, Type: constant.ChannelTypeOpenAI, Key: "fixture-b", BaseURL: &urlB, Models: "model-a,model-b", Group: "fixture-group", Status: common.ChannelStatusEnabled, Priority: &low}}
	require.NoError(t, db.Create(&channels).Error)
	abilities := []model.Ability{}
	for _, name := range []string{"model-a", "model-b"} {
		abilities = append(abilities, model.Ability{Group: "fixture-group", Model: name, ChannelId: 1, Enabled: true, Priority: &high}, model.Ability{Group: "fixture-group", Model: name, ChannelId: 2, Enabled: true, Priority: &low}, model.Ability{Group: "disabled-group", Model: "hidden", ChannelId: 2, Enabled: false})
	}
	// Only one disabled ability has the same composite primary key.
	abilities = abilities[:len(abilities)-1]
	require.NoError(t, db.Create(&abilities).Error)
	groups, err := model.ListChannelProbeGroups()
	require.NoError(t, err)
	require.Len(t, groups, 1)
	assert.Equal(t, []string{"model-a", "model-b"}, groups[0].Models)
	probe := channelProbeFixture()
	probe.Scope, probe.GroupName, probe.ChannelID, probe.Models = "group", "fixture-group", 0, []string{"model-a", "model-b"}
	require.NoError(t, service.ValidateChannelProbe(&probe))
	for _, name := range probe.Models {
		probe.Model = name
		result := service.RunChannelProbeTarget(context.Background(), probe, RunActiveChannelProbe)
		assert.True(t, result.Success, result.ErrorCode)
		assert.Equal(t, name, result.Model)
		require.NotNil(t, result.TTFTMS)
		assert.GreaterOrEqual(t, result.LatencyMS, *result.TTFTMS)
	}
	assert.EqualValues(t, 2, failedCalls.Load())
	assert.EqualValues(t, 2, successfulCalls.Load())
	assert.Equal(t, []string{"model-a", "model-b"}, models)
	common.RetryTimes = 0
	result := RunActiveChannelProbe(context.Background(), probe)
	assert.False(t, result.Success)
	assert.EqualValues(t, 2, successfulCalls.Load(), "zero retries never calls fallback")
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(-time.Second))
	defer cancel()
	result = service.RunChannelProbeTarget(ctx, probe, RunActiveChannelProbe)
	assert.Equal(t, "timeout", result.Status)
	assert.False(t, db.Migrator().HasTable(&model.User{}))
	assert.False(t, db.Migrator().HasTable(&model.Log{}))
	recorder := httptest.NewRecorder()
	c, _ := gin.CreateTestContext(recorder)
	c.Request = httptest.NewRequest(http.MethodGet, "/api/channel/probes/status?hours=2", strings.NewReader(""))
	GetPublicChannelProbeStatus(c)
	assert.Equal(t, http.StatusBadRequest, recorder.Code)
	// Exercise the actual middleware branch: no controller or billing is reached.
	require.NoError(t, i18n.Init())
	require.NoError(t, model.SaveChannelProbe(&probe, true))
	require.NoError(t, service.RefreshChannelProbeCache())
	require.NoError(t, db.Where("enabled = ?", true).Delete(&model.Ability{}).Error)
	engine := gin.New()
	engine.POST("/v1/chat/completions", func(c *gin.Context) {
		common.SetContextKey(c, constant.ContextKeyUsingGroup, "fixture-group")
	}, middleware.Distribute(), func(c *gin.Context) { t.Error("no-channel request reached the relay") })
	failedRequest := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(`{"model":"model-b","messages":[{"role":"user","content":"hi"}]}`))
	failedRequest.Header.Set("Content-Type", "application/json")
	failedRecorder := httptest.NewRecorder()
	engine.ServeHTTP(failedRecorder, failedRequest)
	assert.Equal(t, http.StatusServiceUnavailable, failedRecorder.Code)
	missing, err := model.GetChannelProbeHistory(probe.ID, "model-b")
	require.NoError(t, err)
	require.Len(t, missing, 1)
	assert.Equal(t, "channel_missing", missing[0].ErrorCode)
	assert.False(t, missing[0].Success)
}

func TestChannelProbeModelEditsPreserveRetainedHistory(t *testing.T) {
	channelProbeTestDB(t)
	probe := channelProbeFixture()
	require.NoError(t, model.SaveChannelProbe(&probe, true))
	now := time.Now()
	ms := int64(1200)
	require.NoError(t, model.StoreChannelProbeResult(probe, model.ChannelProbeResult{Source: "passive", CheckedAt: now.Add(-2 * time.Hour).Unix(), Success: true, TTFTMS: &ms}))
	oldRevision := probe
	probe.Models = []string{probe.Model, "new-model"}
	require.NoError(t, model.SaveChannelProbe(&probe, false))
	require.NoError(t, model.StoreChannelProbeResult(oldRevision, model.ChannelProbeResult{Source: "passive", CheckedAt: now.Unix(), Success: false}))
	existing, err := service.BuildPublicChannelProbeModel(probe, "probe-model", now, 24)
	require.NoError(t, err)
	assert.EqualValues(t, 1, existing.SampleCount)
	assert.EqualValues(t, 1, existing.SuccessCount)
	assert.Len(t, existing.History, 1, "adding a model must preserve existing raw history")
	added, err := service.BuildPublicChannelProbeModel(probe, "new-model", now, 24)
	require.NoError(t, err)
	assert.Zero(t, added.SampleCount)
	require.NoError(t, model.StoreChannelProbeResult(probe, model.ChannelProbeResult{Model: "new-model", Source: "passive", CheckedAt: now.Unix(), Success: true, TTFTMS: &ms}))
	probe.Models = []string{"new-model"}
	require.NoError(t, model.SaveChannelProbe(&probe, false))
	removed, err := service.BuildPublicChannelProbeModel(probe, "probe-model", now, 24)
	require.NoError(t, err)
	assert.Zero(t, removed.SampleCount, "removed model aggregates must be deleted")
	assert.Empty(t, removed.History, "removed model raw samples must be deleted")
	retained, err := service.BuildPublicChannelProbeModel(probe, "new-model", now, 24)
	require.NoError(t, err)
	assert.EqualValues(t, 1, retained.SampleCount)
	assert.Len(t, retained.History, 1)
}

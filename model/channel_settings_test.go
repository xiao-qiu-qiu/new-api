package model

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/constant"
	filterdto "github.com/QuantumNous/new-api/dto"
	"github.com/QuantumNous/new-api/relaykit/dto"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestChannelValidateSettingsRejectsInvalidHTTPTransport(t *testing.T) {
	tests := []struct {
		name    string
		setting dto.ChannelSettings
		wantErr string
	}{
		{
			name:    "auto with shards is valid",
			setting: dto.ChannelSettings{HTTPProtocol: "auto", HTTP2ConnectionShards: 4},
		},
		{
			name:    "http1 with shards greater than one rejected",
			setting: dto.ChannelSettings{HTTPProtocol: "http1", HTTP2ConnectionShards: 2},
			wantErr: "http2_connection_shards",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{}
			channel.SetSetting(tt.setting)
			err := channel.ValidateSettings()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestAdvancedCustomChannelRequiresModelListRouteOnlyWhenUpdateChecksEnabled(t *testing.T) {
	inferenceRoute := dto.AdvancedCustomRoute{
		IncomingPath: "/v1/chat/completions",
		UpstreamPath: "/v1/chat/completions",
		Converter:    "none",
	}

	tests := []struct {
		name          string
		checksEnabled bool
		routes        []dto.AdvancedCustomRoute
		wantErr       string
	}{
		{
			name:   "legacy channel without discovery route remains valid",
			routes: []dto.AdvancedCustomRoute{inferenceRoute},
		},
		{
			name:          "enabled checks require discovery route",
			checksEnabled: true,
			routes:        []dto.AdvancedCustomRoute{inferenceRoute},
			wantErr:       dto.AdvancedCustomModelListPath,
		},
		{
			name:          "enabled checks accept discovery route",
			checksEnabled: true,
			routes: []dto.AdvancedCustomRoute{
				inferenceRoute,
				{
					IncomingPath: dto.AdvancedCustomModelListPath,
					UpstreamPath: dto.AdvancedCustomModelListPath,
					Converter:    "none",
				},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			channel := &Channel{Type: constant.ChannelTypeAdvancedCustom}
			channel.SetOtherSettings(dto.ChannelOtherSettings{
				UpstreamModelUpdateCheckEnabled: tt.checksEnabled,
				AdvancedCustom: &dto.AdvancedCustomConfig{
					Routes: tt.routes,
				},
			})

			err := channel.ValidateSettings()
			if tt.wantErr == "" {
				require.NoError(t, err)
				return
			}
			require.Error(t, err)
			assert.Contains(t, err.Error(), tt.wantErr)
		})
	}
}

func TestInferencePresetSettingsAndDatabaseRoundTrip(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := openChannelSettingsTestDatabase(t, dialect, "")
			table := db.Table("inference_preset_channels").Session(&gorm.Session{})
			require.NoError(t, table.AutoMigrate(&Channel{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable("inference_preset_channels")) })
			for _, channelType := range []int{constant.ChannelTypeVLLM, constant.ChannelTypeSGLang} {
				t.Run(fmt.Sprint(channelType), func(t *testing.T) {
					channel := &Channel{Type: channelType, Key: "EMPTY", Name: "inference", Status: common.ChannelStatusEnabled}
					require.NoError(t, channel.ValidateSettings())
					require.NotNil(t, channel.GetOtherSettings().AdvancedCustom)
					require.NoError(t, table.Create(channel).Error)
					for range 2 {
						var loaded Channel
						require.NoError(t, table.First(&loaded, channel.Id).Error)
						assert.Equal(t, channelType, loaded.Type)
						assert.Empty(t, loaded.OtherSettings)
						defaults := loaded.GetOtherSettings().AdvancedCustom
						require.NotNil(t, defaults)
						assert.True(t, defaults.SupportsPath("/v1/messages"))
						assert.Empty(t, loaded.OtherSettings, "reading defaults must not rewrite saved settings")
						defaults.Routes[0].UpstreamPath = "/changed-locally"
						assert.Equal(t, "/v1/chat/completions", loaded.GetOtherSettings().AdvancedCustom.Routes[0].UpstreamPath)
					}
					settings := dto.ChannelOtherSettings{AdvancedCustom: &dto.AdvancedCustomConfig{Routes: []dto.AdvancedCustomRoute{{IncomingPath: "/v1/chat/completions", UpstreamPath: "/custom/chat", Models: []string{"allowed"}, Auth: &dto.AdvancedCustomRouteAuth{Type: "none"}}}}}
					channel.SetOtherSettings(settings)
					require.NoError(t, channel.ValidateSettings())
					require.NoError(t, table.Save(channel).Error)
					for range 2 {
						var loaded Channel
						require.NoError(t, table.First(&loaded, channel.Id).Error)
						actual := loaded.GetOtherSettings().AdvancedCustom
						require.Equal(t, common.GetAdvancedCustomPreset(channelType), actual, "named channels must ignore editable advanced_custom overrides")
						assert.Equal(t, channel.OtherSettings, loaded.OtherSettings)
						for _, tc := range []struct {
							path, model string
							allowed     bool
						}{
							{"/v1/chat/completions", "allowed", true},
							{"/v1/chat/completions", "other", true},
							{"/v1/messages", "allowed", true},
							{"/v1/images/generations", "allowed", false},
						} {
							ok, _ := ChannelSatisfiesFilters(&loaded, tc.model, []filterdto.ChannelFilter{{Kind: filterdto.FilterRequestPath, RequestPath: tc.path}})
							assert.Equal(t, tc.allowed, ok)
						}
						endpoints := getPricingEndpointTypesForAbility(AbilityWithChannel{ChannelType: channelType, Ability: Ability{Model: "allowed", ChannelId: loaded.Id}}, map[int]*dto.AdvancedCustomConfig{loaded.Id: actual})
						expectedEndpoints := []constant.EndpointType{constant.EndpointTypeOpenAI, constant.EndpointTypeOpenAIResponse, constant.EndpointTypeAnthropic, constant.EndpointTypeEmbeddings}
						if channelType == constant.ChannelTypeSGLang {
							expectedEndpoints = append(expectedEndpoints, constant.EndpointTypeJinaRerank)
						}
						assert.ElementsMatch(t, expectedEndpoints, endpoints)
					}
				})
			}
		})
	}
}

func openChannelSettingsTestDatabase(t *testing.T, dialect, tablePrefix string) *gorm.DB {
	t.Helper()
	var driver gorm.Dialector
	switch dialect {
	case "sqlite":
		driver = sqlite.Open(filepath.Join(t.TempDir(), "channels.db"))
	case "mysql":
		dsn := os.Getenv("TEST_MYSQL_DSN")
		if dsn == "" {
			t.Skip("TEST_MYSQL_DSN is not configured")
		}
		driver = mysql.Open(dsn)
	case "postgres":
		dsn := os.Getenv("TEST_POSTGRES_DSN")
		if dsn == "" {
			t.Skip("TEST_POSTGRES_DSN is not configured")
		}
		driver = postgres.Open(dsn)
	}
	db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: tablePrefix}})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	var version string
	query := "SELECT version()"
	if dialect == "sqlite" {
		query = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(query).Scan(&version).Error)
	t.Logf("%s version: %s", dialect, version)
	return db
}

func TestChannelServiceTierSettingsDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := openChannelSettingsTestDatabase(t, dialect, "service_tier_test_")
			require.NoError(t, db.AutoMigrate(&Channel{}, &Ability{}))
			t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&Ability{}, &Channel{})) })
			previousDB, previousType, previousCacheEnabled := DB, common.MainDatabaseType(), common.MemoryCacheEnabled
			channelSyncLock.Lock()
			previousChannels, previousGroups := channelsIDM, group2model2channels
			channelSyncLock.Unlock()
			DB = db
			common.SetMainDatabaseType(common.DatabaseType(dialect))
			initCol()
			t.Cleanup(func() {
				DB = previousDB
				common.SetMainDatabaseType(previousType)
				common.MemoryCacheEnabled = previousCacheEnabled
				initCol()
				channelSyncLock.Lock()
				channelsIDM, group2model2channels = previousChannels, previousGroups
				channelSyncLock.Unlock()
			})
			fast := &Channel{Type: constant.ChannelTypeOpenAI, Key: "test", Name: "fast", Models: "alias,deepseek-v4", Status: common.ChannelStatusEnabled, Group: "default", Priority: common.GetPointer[int64](100), Weight: common.GetPointer[uint](1000)}
			fast.ModelMapping = common.GetPointer(`{"alias":"middle","middle":"provider/gpt-6"}`)
			fast.OtherSettings = `{"supported_service_tiers":["standard"," Fast ","priority"],"future_settings":{"count":9007199254740993}}`
			require.NoError(t, fast.ValidateSettings())
			assert.Equal(t, []string{"default", "priority"}, fast.GetOtherSettings().SupportedServiceTiers)
			assert.Contains(t, fast.OtherSettings, `9007199254740993`)
			ultrafast := &Channel{Type: constant.ChannelTypeOpenAI, Key: "test", Name: "ultrafast", Models: fast.Models, ModelMapping: fast.ModelMapping, Status: common.ChannelStatusEnabled, Group: "default", Priority: common.GetPointer[int64](1), Weight: common.GetPointer[uint](1)}
			ultrafast.SetOtherSettings(dto.ChannelOtherSettings{SupportedServiceTiers: []string{"default", "priority", "ultrafast", "flex"}})
			for _, ch := range []*Channel{fast, ultrafast} {
				require.NoError(t, ch.ValidateSettings())
				require.NoError(t, db.Create(ch).Error)
				var loaded Channel
				require.NoError(t, db.First(&loaded, ch.Id).Error)
				assert.Equal(t, ch.OtherSettings, loaded.OtherSettings)
				for _, name := range []string{"alias", "deepseek-v4"} {
					require.NoError(t, db.Create(&Ability{Group: "default", Model: name, ChannelId: ch.Id, Enabled: true, Priority: ch.Priority, Weight: *ch.Weight}).Error)
				}
			}
			channelSyncLock.Lock()
			channelsIDM = map[int]*Channel{fast.Id: fast, ultrafast.Id: ultrafast}
			group2model2channels = map[string]map[string][]int{"default": {"alias": {fast.Id, ultrafast.Id}, "deepseek-v4": {fast.Id, ultrafast.Id}}}
			channelSyncLock.Unlock()
			for _, memoryCache := range []bool{false, true} {
				common.MemoryCacheEnabled = memoryCache
				for _, tc := range []struct {
					model, tier, policy string
					want                *Channel
				}{
					{"alias", "ultrafast", "follow", ultrafast},
					{"alias", "default", "ultrafast", ultrafast},
					{"alias", "fast", "follow", fast},
					{"deepseek-v4", "ultrafast", "ultrafast", fast},
				} {
					filters := []filterdto.ChannelFilter{{Kind: filterdto.FilterServiceTier, ServiceTier: tc.tier, OpenAITierPolicy: tc.policy}}
					selected, err := GetRandomSatisfiedChannel("default", tc.model, 0, filters)
					require.NoError(t, err)
					require.NotNil(t, selected)
					assert.Equal(t, tc.want.Id, selected.Id, "cache=%v model=%s tier=%s policy=%s", memoryCache, tc.model, tc.tier, tc.policy)
				}
			}
		})
	}
}

func TestChannelValidateSupportedServiceTiers(t *testing.T) {
	for _, raw := range []string{`["priority","unknown"]`, `["default",""]`, `["default",null]`, `"priority"`, `[123]`} {
		t.Run(raw, func(t *testing.T) {
			ch := &Channel{OtherSettings: `{"supported_service_tiers":` + raw + `}`}
			err := ch.ValidateSettings()
			require.Error(t, err)
			assert.Contains(t, err.Error(), "supported_service_tiers")
			assert.Equal(t, `{"supported_service_tiers":`+raw+`}`, ch.OtherSettings)
		})
	}
	for _, raw := range []string{"", `{}`, `{"supported_service_tiers":null}`, `{"supported_service_tiers":[]}`} {
		ch := &Channel{OtherSettings: raw}
		require.NoError(t, ch.ValidateSettings())
		assert.Empty(t, ch.GetOtherSettings().SupportedServiceTiers)
		assert.Equal(t, raw, ch.OtherSettings)
	}
}

package controller

import (
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
	"github.com/QuantumNous/new-api/setting/system_setting"
	"github.com/gin-gonic/gin"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func TestHomepagePackagesValidation(t *testing.T) {
	valid := `{"schemaVersion":1,"active":"landing","packages":[{"schemaVersion":1,"id":"landing","name":"Landing","kind":"html","layout":"content","html":"<h1>Hello</h1>"}]}`
	for _, tc := range []struct {
		name, raw string
		valid     bool
	}{
		{"custom HTML", valid, true},
		{"official", `{"schemaVersion":1,"active":"official","packages":[]}`, true},
		{"codeq", `{"schemaVersion":1,"active":"codeq","packages":[]}`, true},
		{"unknown active", strings.Replace(valid, `"active":"landing"`, `"active":"missing"`, 1), false},
		{"reserved ID", strings.ReplaceAll(valid, `"landing"`, `"official"`), false},
		{"unsupported version", strings.Replace(valid, `"schemaVersion":1`, `"schemaVersion":2`, 1), false},
		{"script URL", `{"schemaVersion":1,"active":"link","packages":[{"schemaVersion":1,"id":"link","name":"Link","kind":"url","layout":"full","url":"javascript:alert(1)"}]}`, false},
		{"URL credentials", `{"schemaVersion":1,"active":"link","packages":[{"schemaVersion":1,"id":"link","name":"Link","kind":"url","layout":"full","url":"https://user:pass@example.com"}]}`, false},
		{"external URL", `{"schemaVersion":1,"active":"link","packages":[{"schemaVersion":1,"id":"link","name":"Link","kind":"url","layout":"full","url":"https://example.com/"}]}`, true},
		{"size limit", strings.Repeat(" ", system_setting.HomepagePackagesMaxBytes+1), false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := system_setting.ParseHomepagePackages(tc.raw)
			if tc.valid {
				require.NoError(t, err)
			} else {
				require.ErrorIs(t, err, system_setting.ErrInvalidHomepagePackages)
			}
		})
	}
}

func TestHomepagePackagesPublicResponseAndHotSwitch(t *testing.T) {
	common.OptionMapRWMutex.Lock()
	previous := common.OptionMap
	common.OptionMap = map[string]string{"HomePageContent": "legacy"}
	common.OptionMapRWMutex.Unlock()
	t.Cleanup(func() { common.OptionMapRWMutex.Lock(); common.OptionMap = previous; common.OptionMapRWMutex.Unlock() })
	engine := gin.New()
	engine.GET("/api/home_page_content", GetHomePageContent)
	first := httptest.NewRecorder()
	engine.ServeHTTP(first, httptest.NewRequest(http.MethodGet, "/api/home_page_content", nil))
	assert.Contains(t, first.Body.String(), `"data":"legacy"`)

	common.OptionMapRWMutex.Lock()
	common.OptionMap[system_setting.HomepagePackagesOption] = `{"schemaVersion":1,"active":"codeq","packages":[{"schemaVersion":1,"id":"draft","name":"Private draft","kind":"html","layout":"content","html":"UNPUBLISHED"}]}`
	common.OptionMapRWMutex.Unlock()
	changed := httptest.NewRecorder()
	request := httptest.NewRequest(http.MethodGet, "/api/home_page_content", nil)
	request.Header.Set("If-None-Match", first.Header().Get("ETag"))
	engine.ServeHTTP(changed, request)
	require.Equal(t, http.StatusOK, changed.Code)
	assert.NotContains(t, changed.Body.String(), "UNPUBLISHED")
	var envelope publicContentResponse
	require.NoError(t, common.Unmarshal(changed.Body.Bytes(), &envelope))
	var pack system_setting.PublicHomepagePackage
	require.NoError(t, common.UnmarshalJsonStr(envelope.Data, &pack))
	assert.Equal(t, "codeq", pack.Package.ID)
	assert.NotEqual(t, first.Header().Get("ETag"), changed.Header().Get("ETag"))

	unchanged := httptest.NewRecorder()
	request.Header.Set("If-None-Match", changed.Header().Get("ETag"))
	engine.ServeHTTP(unchanged, request)
	assert.Equal(t, http.StatusNotModified, unchanged.Code)
	assert.Empty(t, system_setting.ResolveHomepageContent(`{"schemaVersion":1,"active":"official","packages":[]}`, "legacy"))
	assert.Equal(t, "legacy", system_setting.ResolveHomepageContent(`{"schemaVersion":1,"active":"legacy","packages":[]}`, "legacy"))
	assert.Empty(t, system_setting.ResolveHomepageContent(`invalid`, "legacy"))
}

func TestHomepagePackagesInvalidSaveDoesNotPublish(t *testing.T) {
	engine := gin.New()
	engine.PUT("/api/option/", UpdateOption)
	recorder := httptest.NewRecorder()
	engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(`{"key":"HomePagePackages","value":"{}"}`)))
	assert.Contains(t, recorder.Body.String(), `"success":false`)
	assert.Contains(t, recorder.Body.String(), "Invalid homepage package configuration")
}

func TestHomepagePackagesDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var driver gorm.Dialector
			switch dialect {
			case "sqlite":
				driver = sqlite.Open(":memory:")
			case "mysql":
				if os.Getenv("TEST_MYSQL_DSN") == "" {
					t.Skip("TEST_MYSQL_DSN not configured")
				}
				driver = mysql.Open(os.Getenv("TEST_MYSQL_DSN"))
			case "postgres":
				if os.Getenv("TEST_POSTGRES_DSN") == "" {
					t.Skip("TEST_POSTGRES_DSN not configured")
				}
				driver = postgres.Open(os.Getenv("TEST_POSTGRES_DSN"))
			}
			db, err := gorm.Open(driver, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "homepage_test_"}})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			sqlDB.SetMaxOpenConns(1)
			previousDB := model.DB
			model.DB = db
			common.OptionMapRWMutex.Lock()
			previousOptions := common.OptionMap
			common.OptionMap = map[string]string{}
			common.OptionMapRWMutex.Unlock()
			t.Cleanup(func() {
				model.DB = previousDB
				common.OptionMapRWMutex.Lock()
				common.OptionMap = previousOptions
				common.OptionMapRWMutex.Unlock()
				require.NoError(t, db.Migrator().DropTable(&model.Option{}))
				require.NoError(t, sqlDB.Close())
			})
			require.NoError(t, db.AutoMigrate(&model.Option{}))
			var version string
			query := "SELECT version()"
			if dialect == "sqlite" {
				query = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(query).Scan(&version).Error)
			t.Logf("database version: %s", version)
			const official = `{"schemaVersion":1,"active":"official","packages":[]}`
			const codeq = `{"schemaVersion":1,"active":"codeq","packages":[]}`
			large := `{"schemaVersion":1,"active":"large","packages":[{"schemaVersion":1,"id":"large","name":"Large homepage","kind":"html","layout":"content","html":"` + strings.Repeat("主页", 200000) + `"}]}`
			_, err = system_setting.ParseHomepagePackages(large)
			require.NoError(t, err)
			for _, value := range []string{official, codeq, large, official} {
				require.NoError(t, model.UpdateOptionsBulk(map[string]string{system_setting.HomepagePackagesOption: value}))
				var stored model.Option
				require.NoError(t, db.Where(&model.Option{Key: system_setting.HomepagePackagesOption}).First(&stored).Error)
				assert.Equal(t, value, stored.Value)
				assert.Equal(t, value, common.OptionMap[system_setting.HomepagePackagesOption])
			}
			// A storage failure must never acknowledge or publish the new page.
			require.NoError(t, db.Migrator().DropTable(&model.Option{}))
			payload, err := common.Marshal(map[string]string{"key": system_setting.HomepagePackagesOption, "value": codeq})
			require.NoError(t, err)
			engine := gin.New()
			engine.PUT("/api/option/", UpdateOption)
			recorder := httptest.NewRecorder()
			engine.ServeHTTP(recorder, httptest.NewRequest(http.MethodPut, "/api/option/", strings.NewReader(string(payload))))
			assert.Contains(t, recorder.Body.String(), `"success":false`)
			assert.Equal(t, official, common.OptionMap[system_setting.HomepagePackagesOption])
		})
	}
}

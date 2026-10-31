package model

import (
	"fmt"
	"os"
	"sync"
	"testing"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

func openRedemptionTestDatabase(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
	var driver gorm.Dialector
	switch dialect {
	case "sqlite":
		driver = sqlite.Open(":memory:")
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
	db, err := gorm.Open(driver, &gorm.Config{
		NamingStrategy: schema.NamingStrategy{TablePrefix: "redemption_history_test_"},
	})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	t.Cleanup(func() { assert.NoError(t, sqlDB.Close()) })

	var version string
	versionQuery := "SELECT version()"
	if dialect == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database version: %s", version)
	for _, model := range []any{&Redemption{}, &User{}} {
		require.False(t, db.Migrator().HasTable(model), "use empty test tables")
		require.NoError(t, db.AutoMigrate(model))
		t.Cleanup(func() { assert.NoError(t, db.Migrator().DropTable(model)) })
	}
	previousDB, previousType := DB, common.MainDatabaseType()
	DB = db
	common.SetMainDatabaseType(common.DatabaseType(dialect))
	initCol()
	t.Cleanup(func() {
		DB = previousDB
		common.SetMainDatabaseType(previousType)
		initCol()
	})
	return db
}

func TestGetUserRedemptionsDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := openRedemptionTestDatabase(t, dialect)

			codes := []Redemption{
				{UserId: 999, UsedUserId: 101, Status: common.RedemptionCodeStatusUsed, RedeemedTime: 100, Quota: 500_000},
				{UserId: 202, UsedUserId: 101, Status: common.RedemptionCodeStatusUsed, RedeemedTime: 200, Quota: 700_000},
				{UserId: 202, UsedUserId: 101, Status: common.RedemptionCodeStatusUsed, RedeemedTime: 200, Quota: 5_000_000_000},
				{UserId: 999, UsedUserId: 101, Status: common.RedemptionCodeStatusUsed, RedeemedTime: 300, Quota: 6_000_000_000, ExpiredTime: 1},
				{UserId: 101, UsedUserId: 202, Status: common.RedemptionCodeStatusUsed, RedeemedTime: 400, Quota: 900_000},
				{UserId: 101, UsedUserId: 101, Status: common.RedemptionCodeStatusEnabled, RedeemedTime: 500, Quota: 800_000},
				{UserId: 101, UsedUserId: 101, Status: common.RedemptionCodeStatusDisabled, RedeemedTime: 500, Quota: 800_000},
				{UserId: 101, Status: common.RedemptionCodeStatusUsed, RedeemedTime: 600, Quota: 800_000},
			}
			for i := range codes {
				codes[i].Key = fmt.Sprintf("%032d", i+1)
			}
			require.NoError(t, db.Create(&codes).Error)
			require.NoError(t, db.Delete(&codes[3]).Error)

			for _, tc := range []struct {
				name      string
				userId    int
				startIdx  int
				num       int
				wantIDs   []int
				wantTotal int64
				wantQuota int64
			}{
				{name: "first page includes deleted history and sorts timestamp then id", userId: 101, num: 2, wantIDs: []int{codes[3].Id, codes[2].Id}, wantTotal: 4, wantQuota: 11_001_200_000},
				{name: "second page keeps totals for all successful redemptions", userId: 101, startIdx: 2, num: 2, wantIDs: []int{codes[1].Id, codes[0].Id}, wantTotal: 4, wantQuota: 11_001_200_000},
				{name: "past last page returns empty items with full totals", userId: 101, startIdx: 4, num: 2, wantIDs: []int{}, wantTotal: 4, wantQuota: 11_001_200_000},
				{name: "other user sees only their own usage despite creating codes", userId: 202, num: 10, wantIDs: []int{codes[4].Id}, wantTotal: 1, wantQuota: 900_000},
				{name: "creator without usage has no history", userId: 999, num: 10, wantIDs: []int{}},
				{name: "unknown user gets zero totals and empty items", userId: 303, num: 10, wantIDs: []int{}},
				{name: "negative pagination falls back to a bounded first page", userId: 101, startIdx: -1, num: -1, wantIDs: []int{codes[3].Id, codes[2].Id, codes[1].Id, codes[0].Id}, wantTotal: 4, wantQuota: 11_001_200_000},
			} {
				t.Run(tc.name, func(t *testing.T) {
					history, err := GetUserRedemptions(tc.userId, tc.startIdx, tc.num)
					require.NoError(t, err)
					require.NotNil(t, history.Items)
					assert.Equal(t, tc.wantTotal, history.Total)
					assert.Equal(t, tc.wantQuota, history.TotalQuota)
					ids := make([]int, 0, len(history.Items))
					for _, item := range history.Items {
						ids = append(ids, item.Id)
					}
					assert.Equal(t, tc.wantIDs, ids)
				})
			}

			t.Run("history JSON contains only public fields and raw quota units", func(t *testing.T) {
				history, err := GetUserRedemptions(101, 0, 1)
				require.NoError(t, err)
				payload, err := common.Marshal(history)
				require.NoError(t, err)
				assert.JSONEq(t, fmt.Sprintf(`{"items":[{"id":%d,"quota":6000000000,"redeemed_time":300}],"total":4,"total_quota":11001200000}`, codes[3].Id), string(payload))
				emptyHistory, err := GetUserRedemptions(303, 0, 1)
				require.NoError(t, err)
				payload, err = common.Marshal(emptyHistory)
				require.NoError(t, err)
				assert.JSONEq(t, `{"items":[],"total":0,"total_quota":0}`, string(payload))
			})

			t.Run("cleaning invalid codes preserves all successful redemption history", func(t *testing.T) {
				_, err := DeleteInvalidRedemptions()
				require.NoError(t, err)
				history, err := GetUserRedemptions(101, 0, 10)
				require.NoError(t, err)
				assert.Len(t, history.Items, 4)
				assert.EqualValues(t, 4, history.Total)
				assert.EqualValues(t, 11_001_200_000, history.TotalQuota)
			})

			t.Run("missing or invalid identity cannot read unassigned usage", func(t *testing.T) {
				for _, userId := range []int{0, -1} {
					history, err := GetUserRedemptions(userId, 0, 10)
					require.Error(t, err)
					assert.Nil(t, history)
				}
			})
		})
	}
}

func TestSearchRedemptionsFiltersAndPaginates(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			openRedemptionTestDatabase(t, dialect)

			now := common.GetTimestamp()
			redemptions := []Redemption{
				{Id: 1, Name: "alpha-active", Key: "start-shared-active-end", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: 0},
				{Id: 2, Name: "alpha-future", Key: "start-shared-future-end", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: now + 3600},
				{Id: 3, Name: "alpha-expired", Key: "start-shared-expired-end", Status: common.RedemptionCodeStatusEnabled, ExpiredTime: now - 10},
				{Id: 4, Name: "2-disabled", Key: `literal%_!\tail`, Status: common.RedemptionCodeStatusDisabled, ExpiredTime: 0},
				{Id: 5, Name: "beta-used", Key: "12345678901234567890123456789012", Status: common.RedemptionCodeStatusUsed, ExpiredTime: 0},
			}
			require.NoError(t, DB.Create(&redemptions).Error)
			deleted := &Redemption{Id: 6, Name: "alpha-deleted", Key: "start-shared-deleted-end"}
			require.NoError(t, DB.Create(deleted).Error)
			require.NoError(t, DB.Delete(deleted).Error)

			tests := []struct {
				name      string
				keyword   string
				status    string
				startIdx  int
				num       int
				wantTotal int64
				wantIds   []int
			}{
				{name: "complete code", keyword: redemptions[0].Key, num: 10, wantTotal: 1, wantIds: []int{1}},
				{name: "code fragment", keyword: "shared", num: 10, wantTotal: 3, wantIds: []int{3, 2, 1}},
				{name: "numeric keyword matches id name and code", keyword: "2", num: 10, wantTotal: 3, wantIds: []int{5, 4, 2}},
				{name: "numeric full code exceeding integer range", keyword: redemptions[4].Key, num: 10, wantTotal: 1, wantIds: []int{5}},
				{name: "numeric alternatives respect status", keyword: "2", status: "2", num: 10, wantTotal: 1, wantIds: []int{4}},
				{name: "literal percent", keyword: "%", num: 10, wantTotal: 1, wantIds: []int{4}},
				{name: "literal underscore", keyword: "_", num: 10, wantTotal: 1, wantIds: []int{4}},
				{name: "literal escape and backslash", keyword: `%_!\`, num: 10, wantTotal: 1, wantIds: []int{4}},
				{name: "code fragment status and pagination keep filtered total", keyword: "shared", status: "1", startIdx: 1, num: 1, wantTotal: 2, wantIds: []int{1}},
				{name: "code fragment with expired status", keyword: "shared", status: "expired", num: 10, wantTotal: 1, wantIds: []int{3}},
				{name: "unknown code returns no rows", keyword: "missing-code", num: 10, wantIds: []int{}},
				{
					name:      "no filters returns all rows",
					num:       10,
					wantTotal: 5,
					wantIds:   []int{5, 4, 3, 2, 1},
				},
				{
					name:      "keyword filters by name prefix",
					keyword:   "alpha",
					num:       10,
					wantTotal: 3,
					wantIds:   []int{3, 2, 1},
				},
				{
					name:      "enabled status excludes expired rows",
					status:    "1",
					num:       10,
					wantTotal: 2,
					wantIds:   []int{2, 1},
				},
				{
					name:      "expired status returns enabled expired rows",
					status:    "expired",
					num:       10,
					wantTotal: 1,
					wantIds:   []int{3},
				},
				{
					name:      "disabled status",
					status:    "2",
					num:       10,
					wantTotal: 1,
					wantIds:   []int{4},
				},
				{
					name:      "used status",
					status:    "3",
					num:       10,
					wantTotal: 1,
					wantIds:   []int{5},
				},
				{
					name:      "pagination keeps unpaged total",
					startIdx:  1,
					num:       2,
					wantTotal: 5,
					wantIds:   []int{4, 3},
				},
			}

			for _, tt := range tests {
				t.Run(tt.name, func(t *testing.T) {
					rows, total, err := SearchRedemptions(tt.keyword, tt.status, tt.startIdx, tt.num)
					require.NoError(t, err)
					assert.Equal(t, tt.wantTotal, total)
					gotIds := make([]int, 0, len(rows))
					for _, row := range rows {
						gotIds = append(gotIds, row.Id)
					}
					assert.Equal(t, tt.wantIds, gotIds)
				})
			}
		})
	}
}

func TestAdminRedemptionUsersDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := openRedemptionTestDatabase(t, dialect)
			users := []User{
				{Id: 101, Username: "alice", Remark: "管理员备注", Password: "private-password", AffCode: "alice"},
				{Id: 102, Username: "bob", Password: "private-password", AffCode: "bob"},
				{Id: 103, Username: "deleted", Remark: "deleted remark", Password: "private-password", AffCode: "deleted"},
			}
			require.NoError(t, db.Create(&users).Error)
			require.NoError(t, db.Delete(&users[2]).Error)
			codes := []Redemption{
				{UsedUserId: 101}, {UsedUserId: 101}, {UsedUserId: 102},
				{UsedUserId: 103}, {UsedUserId: 999}, {UsedUserId: 0},
			}
			for i := range codes {
				codes[i].Id = i + 1
				codes[i].Name = "user-details"
				codes[i].Key = fmt.Sprintf("%032d", i+1)
				codes[i].Status = common.RedemptionCodeStatusUsed
				codes[i].RedeemedTime = int64(i + 1)
			}
			require.NoError(t, db.Create(&codes).Error)

			for _, endpoint := range []string{"list", "search"} {
				t.Run(endpoint, func(t *testing.T) {
					var rows []*AdminRedemption
					var total int64
					var err error
					if endpoint == "list" {
						rows, total, err = GetAllRedemptions(0, 10)
					} else {
						rows, total, err = SearchRedemptions("user-details", "3", 0, 10)
					}
					require.NoError(t, err)
					assert.EqualValues(t, 6, total)
					require.Len(t, rows, 6)
					for i, row := range rows {
						assert.Equal(t, codes[5-i].Id, row.Id)
						assert.Equal(t, codes[5-i].UsedUserId, row.UsedUserId)
						switch row.UsedUserId {
						case 101:
							assert.Equal(t, "alice", row.UsedUsername)
							assert.Equal(t, "管理员备注", row.UsedUserRemark)
						case 102:
							assert.Equal(t, "bob", row.UsedUsername)
							assert.Empty(t, row.UsedUserRemark)
						default:
							assert.Empty(t, row.UsedUsername)
							assert.Empty(t, row.UsedUserRemark)
						}
					}
					payload, err := common.Marshal(rows)
					require.NoError(t, err)
					var items []map[string]any
					require.NoError(t, common.Unmarshal(payload, &items))
					assert.EqualValues(t, 101, items[4]["used_user_id"])
					assert.Equal(t, "alice", items[4]["used_username"])
					assert.Equal(t, "管理员备注", items[4]["used_user_remark"])
					assert.Equal(t, "", items[0]["used_username"])
					assert.Equal(t, "", items[0]["used_user_remark"])
					assert.NotContains(t, string(payload), "private-password")
				})
			}

			history, err := GetUserRedemptions(101, 0, 10)
			require.NoError(t, err)
			payload, err := common.Marshal(history)
			require.NoError(t, err)
			assert.JSONEq(t, `{"items":[{"id":2,"quota":100,"redeemed_time":2},{"id":1,"quota":100,"redeemed_time":1}],"total":2,"total_quota":200}`, string(payload))
		})
	}
}

func setupRedeemFixture(t *testing.T, quota int) (userId int, key string) {
	t.Helper()
	require.NoError(t, DB.AutoMigrate(&Redemption{}))
	require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
	t.Cleanup(func() {
		require.NoError(t, DB.Session(&gorm.Session{AllowGlobalUpdate: true}).Unscoped().Delete(&Redemption{}).Error)
		DB.Exec("DELETE FROM users")
		DB.Exec("DELETE FROM logs")
	})

	user := &User{Username: "redeem-user", Password: "password", Status: common.UserStatusEnabled, Quota: 0}
	require.NoError(t, DB.Create(user).Error)

	key = "10000000000000000000000000000001"
	redemption := &Redemption{
		Name:        "redeem-test",
		Key:         key,
		Status:      common.RedemptionCodeStatusEnabled,
		Quota:       quota,
		CreatedTime: common.GetTimestamp(),
	}
	require.NoError(t, DB.Create(redemption).Error)
	return user.Id, key
}

func TestRedeemCreditsQuotaExactlyOnce(t *testing.T) {
	userId, key := setupRedeemFixture(t, 500)

	quota, err := Redeem(key, userId)
	require.NoError(t, err)
	assert.Equal(t, 500, quota)

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 500, user.Quota)

	var redemption Redemption
	require.NoError(t, DB.First(&redemption, "name = ?", "redeem-test").Error)
	assert.Equal(t, common.RedemptionCodeStatusUsed, redemption.Status)
	assert.Equal(t, userId, redemption.UsedUserId)

	// Redeeming the same code again must fail and must not credit quota.
	_, err = Redeem(key, userId)
	require.Error(t, err)
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 500, user.Quota)
}

func TestRedeemRejectsWalletOverflow(t *testing.T) {
	userId, key := setupRedeemFixture(t, 11)
	require.NoError(t, DB.Model(&User{}).Where("id = ?", userId).Update("quota", common.MaxWalletQuota-10).Error)

	_, err := Redeem(key, userId)
	require.ErrorIs(t, err, ErrRedeemFailed)

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, common.MaxWalletQuota-10, user.Quota)

	var redemption Redemption
	require.NoError(t, DB.First(&redemption, "key = ?", key).Error)
	assert.Equal(t, common.RedemptionCodeStatusEnabled, redemption.Status)
}

func TestRedemptionQuotaRejectsWalletOverflow(t *testing.T) {
	setupRedeemFixture(t, 500)

	redemption := &Redemption{
		Name:        "overflow-redemption",
		Key:         "10000000000000000000000000000002",
		Status:      common.RedemptionCodeStatusEnabled,
		Quota:       common.MaxWalletQuota + 1,
		CreatedTime: common.GetTimestamp(),
	}
	require.Error(t, redemption.Insert())
}

// Exactly one of several concurrent redeems of the same code may win, and
// quota must be credited exactly once.
func TestRedeemConcurrentSingleSuccess(t *testing.T) {
	userId, key := setupRedeemFixture(t, 300)

	const goroutines = 5
	successes := make([]bool, goroutines)
	var wg sync.WaitGroup
	wg.Add(goroutines)
	for i := range goroutines {
		go func(idx int) {
			defer wg.Done()
			if _, err := Redeem(key, userId); err == nil {
				successes[idx] = true
			}
		}(i)
	}
	wg.Wait()

	successCount := 0
	for _, ok := range successes {
		if ok {
			successCount++
		}
	}
	assert.Equal(t, 1, successCount, "exactly one concurrent redeem should succeed")

	var user User
	require.NoError(t, DB.First(&user, "id = ?", userId).Error)
	assert.Equal(t, 300, user.Quota, "quota must be credited exactly once")
}

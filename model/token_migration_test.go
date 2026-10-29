package model

import (
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/glebarez/sqlite"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

// Token schema from v1.0.0-rc.44, before the quota-query and tier-policy fields.
type releasedTokenBeforeTierPolicy struct {
	Id                 int
	UserId             int    `gorm:"index"`
	Key                string `gorm:"type:varchar(128);uniqueIndex"`
	Status             int    `gorm:"default:1"`
	Name               string `gorm:"index"`
	CreatedTime        int64  `gorm:"bigint"`
	AccessedTime       int64  `gorm:"bigint"`
	ExpiredTime        int64  `gorm:"bigint;default:-1"`
	RemainQuota        int    `gorm:"default:0"`
	UnlimitedQuota     bool
	ModelLimitsEnabled bool
	ModelLimits        string  `gorm:"type:text"`
	AllowIps           *string `gorm:"default:''"`
	UsedQuota          int     `gorm:"default:0"`
	Group              string  `gorm:"default:''"`
	CrossGroupRetry    bool
	AutoGroups         string         `gorm:"type:text"`
	DeletedAt          gorm.DeletedAt `gorm:"index"`
}

func TestTokenOpenAITierPolicyMigration(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			var dialector gorm.Dialector
			switch dialect {
			case "sqlite":
				dialector = sqlite.Open(":memory:")
			case "mysql":
				dsn := os.Getenv("TEST_MYSQL_DSN")
				if dsn == "" {
					t.Skip("TEST_MYSQL_DSN is not configured")
				}
				dialector = mysql.Open(dsn)
			case "postgres":
				dsn := os.Getenv("TEST_POSTGRES_DSN")
				if dsn == "" {
					t.Skip("TEST_POSTGRES_DSN is not configured")
				}
				dialector = postgres.Open(dsn)
			}
			db, err := gorm.Open(dialector, &gorm.Config{})
			require.NoError(t, err)
			sqlDB, err := db.DB()
			require.NoError(t, err)
			t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
			var version string
			versionSQL := "SELECT version()"
			if dialect == "sqlite" {
				versionSQL = "SELECT sqlite_version()"
			}
			require.NoError(t, db.Raw(versionSQL).Scan(&version).Error)
			t.Logf("database version: %s", version)
			for _, legacy := range []bool{false, true} {
				t.Run(fmt.Sprintf("released_schema=%v", legacy), func(t *testing.T) {
					table := fmt.Sprintf("token_tier_%d", time.Now().UnixNano())
					t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(table)) })
					recorder := &migrationSQLRecorder{}
					tableDB := db.Table(table).Session(&gorm.Session{Logger: recorder})
					if legacy {
						require.NoError(t, tableDB.AutoMigrate(&releasedTokenBeforeTierPolicy{}))
						require.NoError(t, tableDB.Create(&releasedTokenBeforeTierPolicy{UserId: 7, Key: "preserved-key", Name: "preserved-name", RemainQuota: 120, UsedQuota: 30, ModelLimitsEnabled: true, ModelLimits: "allowed-model", Group: "auto", AutoGroups: `["vip"]`}).Error)
						assert.False(t, tableDB.Migrator().HasColumn(&releasedTokenBeforeTierPolicy{}, "openai_tier_policy"))
					}
					for round := range 2 {
						recorder.reset()
						require.NoError(t, tableDB.AutoMigrate(&Token{}))
						if round == 1 {
							assert.Empty(t, recorder.schemaMutations(), "second migration must not mutate the schema")
						}
					}
					if legacy {
						var saved Token
						require.NoError(t, tableDB.First(&saved, 1).Error)
						assert.Equal(t, "preserved-key", saved.Key)
						assert.Equal(t, "preserved-name", saved.Name)
						assert.Equal(t, 7, saved.UserId)
						assert.Equal(t, 120, saved.RemainQuota)
						assert.Equal(t, 30, saved.UsedQuota)
						assert.True(t, saved.ModelLimitsEnabled)
						assert.Equal(t, "allowed-model", saved.ModelLimits)
						assert.Equal(t, "auto", saved.Group)
						assert.Equal(t, `["vip"]`, saved.AutoGroups)
						assert.Equal(t, OpenAITierPolicyFollow, saved.OpenAITierPolicy)
					}
					for _, column := range []string{"key", "user_id", "name", "deleted_at"} {
						assert.True(t, db.Migrator().HasIndex(table, db.NamingStrategy.IndexName(table, column)))
					}
					originalDB := DB
					DB = tableDB
					t.Cleanup(func() { DB = originalDB })
					token := Token{UserId: 8, Key: "new-key", UnlimitedQuota: true, ExpiredTime: -1}
					require.NoError(t, token.Insert())
					assert.Equal(t, OpenAITierPolicyFollow, token.OpenAITierPolicy)
					for _, policy := range []string{OpenAITierPolicyPriority, OpenAITierPolicyUltrafast, OpenAITierPolicyFollow} {
						token.OpenAITierPolicy = policy
						require.NoError(t, token.Update())
						var saved Token
						require.NoError(t, tableDB.First(&saved, token.Id).Error)
						assert.Equal(t, policy, saved.OpenAITierPolicy)
					}
					for _, legacyValue := range []any{"", nil} {
						require.NoError(t, tableDB.Where("id = ?", token.Id).Update("openai_tier_policy", legacyValue).Error)
						var saved Token
						require.NoError(t, tableDB.First(&saved, token.Id).Error)
						assert.Equal(t, OpenAITierPolicyFollow, saved.OpenAITierPolicy)
					}
					require.Error(t, tableDB.Create(&Token{UserId: 9, Key: token.Key}).Error, "key uniqueness must survive migration")
					var count int64
					require.NoError(t, tableDB.Model(&Token{}).Count(&count).Error)
					wantCount := int64(1)
					if legacy {
						wantCount++
					}
					assert.Equal(t, wantCount, count)
				})
			}
		})
	}
}

func requireTokenConstraintExists(t *testing.T, db *gorm.DB, constraintName string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
SELECT count(*)
FROM pg_catalog.pg_constraint
WHERE conrelid = to_regclass(?)
  AND conname = ?`, "tokens", constraintName).Scan(&count).Error)
	require.EqualValues(t, 1, count)
}

func requireTokenIndexExists(t *testing.T, db *gorm.DB, indexName string) {
	t.Helper()
	var count int64
	require.NoError(t, db.Raw(`
SELECT count(*)
FROM pg_catalog.pg_index AS index_meta
JOIN pg_catalog.pg_class AS index_class
  ON index_class.oid = index_meta.indexrelid
WHERE index_meta.indrelid = to_regclass(?)
  AND index_class.relname = ?`, "tokens", indexName).Scan(&count).Error)
	require.EqualValues(t, 1, count)
}

func testTokenKeyMigrationNonPostgreSQL(t *testing.T, db *gorm.DB) {
	t.Helper()
	tableName := fmt.Sprintf("token_migration_%d", time.Now().UnixNano())
	t.Cleanup(func() { _ = db.Migrator().DropTable(tableName) })

	tableDB := db.Table(tableName)
	require.NoError(t, tableDB.AutoMigrate(&Token{}))
	require.NoError(t, tableDB.Create(&Token{UserId: 1, Key: "preserved-key"}).Error)

	for range 2 {
		require.NoError(t, migrateTokenKeyUniqueness(db))
		require.NoError(t, tableDB.AutoMigrate(&Token{}))
	}

	var preserved Token
	require.NoError(t, tableDB.Where(&Token{Key: "preserved-key"}).First(&preserved).Error)
	assert.Equal(t, 1, preserved.UserId)
	expectedIndex := db.NamingStrategy.IndexName(tableName, "key")
	assert.True(t, db.Migrator().HasIndex(tableName, expectedIndex))
}

func TestMigrateTokenKeyUniquenessSQLite(t *testing.T) {
	db, err := gorm.Open(sqlite.Open(":memory:"), &gorm.Config{})
	require.NoError(t, err)
	testTokenKeyMigrationNonPostgreSQL(t, db)
}

func TestMigrateTokenKeyUniquenessMySQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_MYSQL_DSN"))
	if dsn == "" {
		t.Skip("TEST_MYSQL_DSN is not configured")
	}

	db, err := gorm.Open(mysql.Open(dsn), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })
	testTokenKeyMigrationNonPostgreSQL(t, db)
}

func TestMigrateTokenKeyUniquenessPostgreSQL(t *testing.T) {
	dsn := strings.TrimSpace(os.Getenv("TEST_POSTGRES_DSN"))
	if dsn == "" {
		t.Skip("TEST_POSTGRES_DSN is not configured")
	}

	db, err := gorm.Open(postgres.New(postgres.Config{
		DSN:                  dsn,
		PreferSimpleProtocol: true,
	}), &gorm.Config{})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	t.Cleanup(func() { require.NoError(t, sqlDB.Close()) })

	tests := []struct {
		name                 string
		prepareOld           func(*testing.T, *gorm.DB)
		expectedError        string
		preservedConstraints []string
		preservedIndexes     []string
	}{
		{name: "fresh"},
		{
			name: "legacy_idx_constraint",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				require.NoError(t, tx.Migrator().DropIndex(&Token{}, tokenKeyIndex))
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?)",
					clause.Table{Name: "tokens"},
					clause.Column{Name: tokenKeyIndex},
					clause.Column{Name: "key"},
				).Error)
			},
		},
		{
			name: "gorm_generated_constraint",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?)",
					clause.Table{Name: "tokens"},
					clause.Column{Name: gormTokenKeyConstraint},
					clause.Column{Name: "key"},
				).Error)
			},
		},
		{
			name: "postgres_default_constraint_without_target_index",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				require.NoError(t, tx.Migrator().DropIndex(&Token{}, tokenKeyIndex))
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?)",
					clause.Table{Name: "tokens"},
					clause.Column{Name: postgresTokenKeyConstraint},
					clause.Column{Name: "key"},
				).Error)
			},
		},
		{
			name: "non_conflicting_uniqueness_is_preserved",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?)",
					clause.Table{Name: "tokens"},
					clause.Column{Name: postgresTokenKeyConstraint},
					clause.Column{Name: "key"},
				).Error)
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?, ?)",
					clause.Table{Name: "tokens"},
					clause.Column{Name: "keep_tokens_key_user_id"},
					clause.Column{Name: "key"},
					clause.Column{Name: "user_id"},
				).Error)
				require.NoError(t, tx.Exec(
					"CREATE UNIQUE INDEX ? ON ? (?) WHERE user_id > 0",
					clause.Column{Name: "keep_tokens_partial_key"},
					clause.Table{Name: "tokens"},
					clause.Column{Name: "key"},
				).Error)
			},
			preservedConstraints: []string{"keep_tokens_key_user_id"},
			preservedIndexes:     []string{"keep_tokens_partial_key"},
		},
		{
			name: "arbitrary_constraint_is_rejected",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?)",
					clause.Table{Name: "tokens"},
					clause.Column{Name: "keep_tokens_key_unique"},
					clause.Column{Name: "key"},
				).Error)
			},
			expectedError:        "unsupported unique constraint",
			preservedConstraints: []string{"keep_tokens_key_unique"},
		},
		{
			name: "deferrable_constraint_is_rejected",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				require.NoError(t, tx.Migrator().DropIndex(&Token{}, tokenKeyIndex))
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?) DEFERRABLE INITIALLY DEFERRED",
					clause.Table{Name: "tokens"},
					clause.Column{Name: postgresTokenKeyConstraint},
					clause.Column{Name: "key"},
				).Error)
			},
			expectedError:        "unsupported definition",
			preservedConstraints: []string{postgresTokenKeyConstraint},
		},
		{
			name: "invalid_target_index_is_rejected",
			prepareOld: func(t *testing.T, tx *gorm.DB) {
				t.Helper()
				require.NoError(t, tx.Migrator().DropIndex(&Token{}, tokenKeyIndex))
				require.NoError(t, tx.Exec(
					"CREATE INDEX ? ON ? (?)",
					clause.Column{Name: tokenKeyIndex},
					clause.Table{Name: "tokens"},
					clause.Column{Name: "key"},
				).Error)
				require.NoError(t, tx.Exec(
					"ALTER TABLE ? ADD CONSTRAINT ? UNIQUE (?)",
					clause.Table{Name: "tokens"},
					clause.Column{Name: postgresTokenKeyConstraint},
					clause.Column{Name: "key"},
				).Error)
			},
			expectedError:        "unexpected definition",
			preservedConstraints: []string{postgresTokenKeyConstraint},
			preservedIndexes:     []string{tokenKeyIndex},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			tx := db.Begin()
			require.NoError(t, tx.Error)
			t.Cleanup(func() { _ = tx.Rollback().Error })

			schemaName := fmt.Sprintf("token_migration_%d", time.Now().UnixNano())
			require.NoError(t, tx.Exec(
				"CREATE SCHEMA ?",
				clause.Table{Name: schemaName},
			).Error)
			require.NoError(t, tx.Exec(
				"SET LOCAL search_path TO ?",
				clause.Table{Name: schemaName},
			).Error)

			require.NoError(t, migrateTokenKeyUniqueness(tx))
			require.NoError(t, tx.AutoMigrate(&Token{}))
			original := Token{UserId: 1, Key: "preserved-key", Name: "preserve me"}
			require.NoError(t, tx.Create(&original).Error)
			if test.prepareOld != nil {
				test.prepareOld(t, tx)
			}

			if test.expectedError != "" {
				err := migrateTokenKeyUniqueness(tx)
				require.Error(t, err)
				assert.Contains(t, err.Error(), test.expectedError)
				for _, constraintName := range test.preservedConstraints {
					requireTokenConstraintExists(t, tx, constraintName)
				}
				for _, indexName := range test.preservedIndexes {
					requireTokenIndexExists(t, tx, indexName)
				}
				return
			}

			for range 2 {
				require.NoError(t, migrateTokenKeyUniqueness(tx))
				require.NoError(t, tx.AutoMigrate(&Token{}))
			}

			var preserved Token
			require.NoError(t, tx.First(&preserved, original.Id).Error)
			assert.Equal(t, original.Key, preserved.Key)
			assert.Equal(t, original.Name, preserved.Name)

			constraints, err := inspectTokenKeyUniqueConstraints(tx, "tokens")
			require.NoError(t, err)
			assert.Empty(t, constraints)
			targetIndex, err := inspectTokenKeyIndex(tx, "tokens")
			require.NoError(t, err)
			assert.True(t, targetIndex.standaloneValid)
			for _, constraintName := range test.preservedConstraints {
				requireTokenConstraintExists(t, tx, constraintName)
			}
			for _, indexName := range test.preservedIndexes {
				requireTokenIndexExists(t, tx, indexName)
			}

			duplicateError := tx.Transaction(func(duplicateTx *gorm.DB) error {
				return duplicateTx.Create(&Token{UserId: 2, Key: original.Key}).Error
			})
			require.Error(t, duplicateError)

			var totalRows int64
			require.NoError(t, tx.Model(&Token{}).Count(&totalRows).Error)
			assert.EqualValues(t, 1, totalRows)
		})
	}
}

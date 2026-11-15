package model

import (
	"context"
	"fmt"
	"os"
	"sync"
	"testing"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/glebarez/sqlite"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"gorm.io/driver/mysql"
	"gorm.io/driver/postgres"
	"gorm.io/gorm"
	"gorm.io/gorm/schema"
)

// useSystemTaskMatrixDB points DB at a fresh system_tasks table on dialect,
// skipping MySQL and PostgreSQL unless TEST_MYSQL_DSN / TEST_POSTGRES_DSN are set.
func useSystemTaskMatrixDB(t *testing.T, dialect string) *gorm.DB {
	t.Helper()
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
	db, err := gorm.Open(dialector, &gorm.Config{NamingStrategy: schema.NamingStrategy{TablePrefix: "history_test_"}})
	require.NoError(t, err)
	sqlDB, err := db.DB()
	require.NoError(t, err)
	sqlDB.SetMaxOpenConns(1)
	previousDB := DB
	DB = db
	t.Cleanup(func() {
		DB = previousDB
		require.NoError(t, db.Migrator().DropTable(&SystemTask{}))
		require.NoError(t, sqlDB.Close())
	})
	require.NoError(t, db.AutoMigrate(&SystemTask{}))
	var version string
	versionQuery := "SELECT version()"
	if dialect == "sqlite" {
		versionQuery = "SELECT sqlite_version()"
	}
	require.NoError(t, db.Raw(versionQuery).Scan(&version).Error)
	t.Logf("database version: %s", version)
	return db
}

func TestSystemTaskHistoryDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useSystemTaskMatrixDB(t, dialect)

			tasks := []SystemTask{
				{TaskID: "model-failed-old", Type: SystemTaskTypeModelUpdate, Status: SystemTaskStatusFailed},
				{TaskID: "model-success", Type: SystemTaskTypeModelUpdate, Status: SystemTaskStatusSucceeded},
				{TaskID: "channel-failed", Type: SystemTaskTypeChannelTest, Status: SystemTaskStatusFailed},
				{TaskID: "model-failed-recent", Type: SystemTaskTypeModelUpdate, Status: SystemTaskStatusFailed},
				{TaskID: "model-latest", Type: SystemTaskTypeModelUpdate, Status: SystemTaskStatusSucceeded},
				{TaskID: "channel-pending", Type: SystemTaskTypeChannelTest, Status: SystemTaskStatusPending},
				{TaskID: "channel-running", Type: SystemTaskTypeChannelTest, Status: SystemTaskStatusRunning},
				{TaskID: "log-only-history", Type: SystemTaskTypeLogCleanup, Status: SystemTaskStatusSucceeded},
			}
			require.NoError(t, db.Create(&tasks).Error)
			filter := SystemTaskFilter{Scope: "history", Type: SystemTaskTypeModelUpdate, Status: SystemTaskStatusFailed}
			page, total, err := ListSystemTasks(filter, 1, 1)
			require.NoError(t, err)
			assert.EqualValues(t, 2, total)
			require.Len(t, page, 1)
			assert.Equal(t, "model-failed-old", page[0].TaskID)
			page, total, err = ListSystemTasks(filter, 2, 1)
			require.NoError(t, err)
			assert.EqualValues(t, 2, total)
			assert.Empty(t, page)

			active, total, err := ListSystemTasks(SystemTaskFilter{Scope: "active"}, 0, 20)
			require.NoError(t, err)
			assert.EqualValues(t, 2, total)
			require.Len(t, active, 2)
			assert.Equal(t, "channel-running", active[0].TaskID)
			assert.Equal(t, "channel-pending", active[1].TaskID)

			deleted, err := DeleteSystemTaskHistory(filter)
			require.NoError(t, err)
			assert.EqualValues(t, 2, deleted)
			_, total, err = ListSystemTasks(SystemTaskFilter{Scope: "history"}, 0, 20)
			require.NoError(t, err)
			assert.EqualValues(t, 4, total, "other types and statuses are untouched")

			deleted, err = DeleteSystemTaskHistory(SystemTaskFilter{Scope: "active", Status: SystemTaskStatusRunning})
			require.NoError(t, err)
			assert.Zero(t, deleted, "caller cannot override the terminal-status restriction")
			deleted, err = DeleteSystemTaskHistory(SystemTaskFilter{})
			require.NoError(t, err)
			assert.EqualValues(t, 2, deleted)
			remaining, total, err := ListSystemTasks(SystemTaskFilter{}, 0, 100)
			require.NoError(t, err)
			assert.EqualValues(t, 4, total)
			var remainingIDs []string
			for _, task := range remaining {
				remainingIDs = append(remainingIDs, task.TaskID)
			}
			assert.ElementsMatch(t, []string{"model-latest", "channel-pending", "channel-running", "log-only-history"}, remainingIDs)
			latest, err := GetLatestSystemTasks([]string{SystemTaskTypeModelUpdate, SystemTaskTypeChannelTest, SystemTaskTypeLogCleanup})
			require.NoError(t, err)
			assert.Equal(t, "model-latest", latest[SystemTaskTypeModelUpdate].TaskID)
			assert.Equal(t, "channel-running", latest[SystemTaskTypeChannelTest].TaskID)
			assert.Equal(t, "log-only-history", latest[SystemTaskTypeLogCleanup].TaskID)
			deleted, err = DeleteSystemTaskHistory(SystemTaskFilter{})
			require.NoError(t, err)
			assert.Zero(t, deleted, "repeated cleanup preserves the scheduler's latest runs")
		})
	}
}

// The daily cleanup deletes finished rows older than the retention in
// batches, and keeps active rows, newer rows and the newest row of each type.
func TestDeleteFinishedSystemTasksDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		t.Run(dialect, func(t *testing.T) {
			db := useSystemTaskMatrixDB(t, dialect)
			const cutoff int64 = 1_000_000
			old := make([]SystemTask, systemTaskCleanupBatchSize+1)
			for i := range old {
				old[i] = SystemTask{TaskID: fmt.Sprintf("sync-old-%d", i), Type: SyncTaskType(SyncTopicChannels), Status: SystemTaskStatusSucceeded, CreatedAt: 10, UpdatedAt: cutoff - 1}
			}
			require.NoError(t, db.CreateInBatches(&old, 200).Error)
			kept := []SystemTask{
				{TaskID: "test-old-pending", Type: SystemTaskTypeChannelTest, Status: SystemTaskStatusPending, CreatedAt: 10, UpdatedAt: 10},
				{TaskID: "test-old-running", Type: SystemTaskTypeChannelTest, Status: SystemTaskStatusRunning, CreatedAt: 10, UpdatedAt: 10},
				{TaskID: "sync-at-cutoff", Type: SyncTaskType(SyncTopicChannels), Status: SystemTaskStatusSucceeded, CreatedAt: 10, UpdatedAt: cutoff},
				{TaskID: "sync-newest", Type: SyncTaskType(SyncTopicChannels), Status: SystemTaskStatusFailed, CreatedAt: 10, UpdatedAt: 10},
				{TaskID: "log-newest", Type: SystemTaskTypeLogCleanup, Status: SystemTaskStatusFailed, CreatedAt: 10, UpdatedAt: 10},
			}
			deletedRows := []SystemTask{
				{TaskID: "log-old-failed", Type: SystemTaskTypeLogCleanup, Status: SystemTaskStatusFailed, CreatedAt: 10, UpdatedAt: 10},
			}
			require.NoError(t, db.Create(&deletedRows).Error)
			require.NoError(t, db.Create(&kept).Error)

			deleted, err := DeleteFinishedSystemTasks(context.Background(), cutoff)
			require.NoError(t, err)
			assert.EqualValues(t, len(old)+len(deletedRows), deleted)
			var remaining []string
			require.NoError(t, db.Model(&SystemTask{}).Order("id").Pluck("task_id", &remaining).Error)
			assert.Equal(t, []string{"test-old-pending", "test-old-running", "sync-at-cutoff", "sync-newest", "log-newest"}, remaining)

			deleted, err = DeleteFinishedSystemTasks(context.Background(), cutoff)
			require.NoError(t, err)
			assert.Zero(t, deleted)
		})
	}
}

// A waiter must keep using the same topic lock after the previous writer
// commits. Otherwise a third writer can commit a higher revision before the
// waiter, and a node that applied it never reloads the waiter's later commit.
func TestSyncTopicRevisionCommitOrderDatabaseMatrix(t *testing.T) {
	for _, dialect := range []string{"sqlite", "mysql", "postgres"} {
		for _, existingAnchor := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/existing_anchor=%t", dialect, existingAnchor), func(t *testing.T) {
				db := useSystemTaskMatrixDB(t, dialect)
				previousType := common.MainDatabaseType()
				common.SetMainDatabaseType(map[string]common.DatabaseType{"sqlite": common.DatabaseTypeSQLite, "mysql": common.DatabaseTypeMySQL, "postgres": common.DatabaseTypePostgreSQL}[dialect])
				t.Cleanup(func() { common.SetMainDatabaseType(previousType) })
				require.NoError(t, db.AutoMigrate(&SystemTaskLock{}, &Option{}))
				t.Cleanup(func() { require.NoError(t, db.Migrator().DropTable(&SystemTaskLock{}, &Option{})) })
				const topic = SyncTopicOAuthProviders
				if existingAnchor {
					require.NoError(t, db.Transaction(func(tx *gorm.DB) error { return bumpSyncTopic(tx, topic) }))
					// Both automatic and manual task-history cleanup leave the
					// fixed topic row available to every subsequent transaction.
					require.NoError(t, ExpireStaleSystemTaskLocks(common.GetTimestamp()+1))
					var anchor SystemTaskLock
					require.NoError(t, db.First(&anchor, "type = ?", SyncTaskType(topic)).Error)
					assert.Empty(t, anchor.TaskID)
				}

				// SQLite permits only one writer. Exercise its real transaction
				// commit/rollback behavior without pretending it has row locks.
				if dialect == "sqlite" {
					for _, key := range []string{"first", "rolled-back", "last"} {
						err := db.Transaction(func(tx *gorm.DB) error {
							if err := tx.Create(&Option{Key: key, Value: key}).Error; err != nil {
								return err
							}
							if err := bumpSyncTopic(tx, topic); err != nil {
								return err
							}
							if key == "rolled-back" {
								return ErrSystemTaskLockLost
							}
							return nil
						})
						if key == "rolled-back" {
							require.ErrorIs(t, err, ErrSystemTaskLockLost)
						} else {
							require.NoError(t, err)
						}
					}
					var count int64
					require.NoError(t, db.Model(&Option{}).Count(&count).Error)
					assert.EqualValues(t, 2, count)
					require.NoError(t, db.Model(&SystemTask{}).Where("type = ?", SyncTaskType(topic)).Count(&count).Error)
					expectedTasks := 2
					if existingAnchor {
						expectedTasks++
					}
					assert.EqualValues(t, expectedTasks, count)
					return
				}

				pool, err := db.DB()
				require.NoError(t, err)
				pool.SetMaxOpenConns(8)
				ctx, cancel := context.WithTimeout(t.Context(), 10*time.Second)
				var workers sync.WaitGroup
				t.Cleanup(func() { cancel(); workers.Wait() })
				type writer struct {
					pid      chan int64
					inserted chan struct{}
					release  func()
					finished chan struct{}
					err      error
				}
				start := func(key string) *writer {
					release := make(chan struct{})
					w := &writer{pid: make(chan int64, 1), inserted: make(chan struct{}), release: sync.OnceFunc(func() { close(release) }), finished: make(chan struct{})}
					t.Cleanup(w.release)
					workers.Go(func() {
						defer close(w.finished)
						w.err = db.WithContext(ctx).Transaction(func(tx *gorm.DB) error {
							query := "SELECT pg_backend_pid()"
							if dialect == "mysql" {
								query = "SELECT CONNECTION_ID()"
							}
							var pid int64
							if err := tx.Raw(query).Scan(&pid).Error; err != nil {
								return err
							}
							w.pid <- pid
							if err := tx.Create(&Option{Key: key, Value: key}).Error; err != nil {
								return err
							}
							if err := bumpSyncTopic(tx, topic); err != nil {
								return err
							}
							close(w.inserted)
							select {
							case <-release:
								return nil
							case <-ctx.Done():
								return ctx.Err()
							}
						})
					})
					return w
				}
				wait := func(ready <-chan struct{}) {
					select {
					case <-ready:
					case <-ctx.Done():
						t.Fatal("transaction barrier timed out")
					}
				}
				waitForLock := func(w *writer) {
					var pid int64
					select {
					case pid = <-w.pid:
					case <-w.finished:
						require.NoError(t, w.err)
						t.Fatal("writer finished without opening its transaction")
					case <-ctx.Done():
						t.Fatal("writer did not open its transaction")
					}
					t.Logf("waiting for transaction connection %d", pid)
					pollInterval := 5 * time.Millisecond
					if dialect == "mysql" {
						// InnoDB caches these diagnostic tables; leave enough time
						// between reads for a lock-wait snapshot to refresh.
						pollInterval = 200 * time.Millisecond
					}
					require.Eventually(t, func() bool {
						select {
						case <-w.finished:
							return true
						default:
						}
						var waiting int64
						query := "SELECT count(*) FROM pg_stat_activity WHERE pid = ? AND wait_event_type = 'Lock'"
						if dialect == "mysql" {
							query = "SELECT count(*) FROM information_schema.innodb_lock_waits w JOIN information_schema.innodb_trx t ON t.trx_id = w.requesting_trx_id WHERE t.trx_mysql_thread_id = ?"
						}
						err := db.Raw(query, pid).Scan(&waiting).Error
						require.NoError(t, err, "database test user must be allowed to inspect lock waits")
						return waiting > 0
					}, 5*time.Second, pollInterval, "writer must wait on the database lock")
					select {
					case <-w.finished:
						require.NoError(t, w.err)
						t.Fatal("a later revision committed before the writer holding the topic lock")
					default:
					}
				}
				first := start("first")
				wait(first.inserted)
				second := start("second")
				waitForLock(second)
				first.release()
				wait(first.finished)
				require.NoError(t, first.err)
				wait(second.inserted)
				last := start("last")
				last.release()
				waitForLock(last)
				before, err := GetLatestSyncRevisions()
				require.NoError(t, err)
				var visible int64
				require.NoError(t, db.Model(&Option{}).Count(&visible).Error)
				assert.EqualValues(t, 1, visible)
				second.release()
				wait(second.finished)
				wait(last.finished)
				require.NoError(t, second.err)
				require.NoError(t, last.err)
				after, err := GetLatestSyncRevisions()
				require.NoError(t, err)
				assert.Greater(t, after[topic], before[topic])
				require.NoError(t, db.Model(&Option{}).Count(&visible).Error)
				assert.EqualValues(t, 3, visible)
			})
		}
	}
}

type testSystemTaskPayload struct {
	TargetTimestamp int64 `json:"target_timestamp"`
	BatchSize       int   `json:"batch_size"`
}

type testSystemTaskState struct {
	Total     int64 `json:"total"`
	Processed int64 `json:"processed"`
	Progress  int   `json:"progress"`
	Remaining int64 `json:"remaining"`
}

func createLegacyPendingSystemTask(t *testing.T, taskType string) *SystemTask {
	t.Helper()
	taskID, err := GenerateSystemTaskID()
	require.NoError(t, err)
	task := &SystemTask{
		TaskID: taskID,
		Type:   taskType,
		Status: SystemTaskStatusPending,
	}
	require.NoError(t, DB.Create(task).Error)
	return task
}

func TestSystemTaskCreateAndActiveLifecycle(t *testing.T) {
	truncateTables(t)

	payload := testSystemTaskPayload{TargetTimestamp: 1000, BatchSize: 100}
	state := testSystemTaskState{}
	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, payload, state)
	require.NoError(t, err)
	require.NotNil(t, task.ActiveKey)
	assert.Equal(t, SystemTaskTypeLogCleanup, *task.ActiveKey)

	var decodedPayload testSystemTaskPayload
	require.NoError(t, task.DecodePayload(&decodedPayload))
	assert.Equal(t, payload, decodedPayload)

	activeTask, err := GetActiveSystemTask(SystemTaskTypeLogCleanup)
	require.NoError(t, err)
	require.NotNil(t, activeTask)
	assert.Equal(t, task.TaskID, activeTask.TaskID)

	runnerID := "runner-a"
	claimedTask, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	err = FinishSystemTask(claimedTask.TaskID, runnerID, SystemTaskStatusSucceeded, map[string]int64{"deleted_count": 0}, "")
	require.NoError(t, err)

	finishedTask, err := GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, finishedTask)
	assert.Nil(t, finishedTask.ActiveKey)

	activeTask, err = GetActiveSystemTask(SystemTaskTypeLogCleanup)
	require.NoError(t, err)
	require.Nil(t, activeTask)

	_, err = CreateSystemTask(SystemTaskTypeLogCleanup, payload, state)
	require.NoError(t, err)
}

func TestSystemTaskActiveKeyPreventsDuplicateActiveRun(t *testing.T) {
	truncateTables(t)

	payload := testSystemTaskPayload{TargetTimestamp: 1000, BatchSize: 100}
	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, payload, testSystemTaskState{})
	require.NoError(t, err)
	_, err = CreateSystemTask(SystemTaskTypeLogCleanup, payload, testSystemTaskState{})
	require.Error(t, err)

	activeTask, err := GetActiveSystemTask(SystemTaskTypeLogCleanup)
	require.NoError(t, err)
	require.NotNil(t, activeTask)
	assert.Equal(t, task.TaskID, activeTask.TaskID)
}

func TestSystemTaskLockPreventsConcurrentClaim(t *testing.T) {
	truncateTables(t)

	payload := testSystemTaskPayload{TargetTimestamp: 1000, BatchSize: 100}
	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, payload, testSystemTaskState{})
	require.NoError(t, err)
	secondTask := createLegacyPendingSystemTask(t, SystemTaskTypeLogCleanup)

	claimedTask, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	_, claimed, err = ClaimSystemTask(secondTask.ID, SystemTaskTypeLogCleanup, "runner-b", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.False(t, claimed)

	assert.Equal(t, "runner-a", claimedTask.LockedBy)

	reloadedSecond, err := GetSystemTaskByTaskID(secondTask.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedSecond)
	assert.Equal(t, SystemTaskStatusPending, reloadedSecond.Status)
}

func TestExpiredSystemTaskLockFailsOldRunAndClaimsLegacyPendingRun(t *testing.T) {
	truncateTables(t)

	first, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	_, claimed, err := ClaimSystemTask(first.ID, SystemTaskTypeLogCleanup, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, DB.Model(&SystemTaskLock{}).
		Where("task_id = ?", first.TaskID).
		Update("locked_until", common.GetTimestamp()-1).Error)

	second := createLegacyPendingSystemTask(t, SystemTaskTypeLogCleanup)
	claimedTask, claimed, err := ClaimSystemTask(second.ID, SystemTaskTypeLogCleanup, "runner-b", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	assert.Equal(t, second.TaskID, claimedTask.TaskID)
	assert.Equal(t, "runner-b", claimedTask.LockedBy)

	reloadedFirst, err := GetSystemTaskByTaskID(first.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedFirst)
	assert.Equal(t, SystemTaskStatusFailed, reloadedFirst.Status)
	assert.Equal(t, "task lease expired", reloadedFirst.Error)
	assert.Nil(t, reloadedFirst.ActiveKey)
}

func TestExpireStaleSystemTaskLockFailsOldRunAndAllowsNewRun(t *testing.T) {
	truncateTables(t)

	first, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	_, claimed, err := ClaimSystemTask(first.ID, SystemTaskTypeLogCleanup, "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, DB.Model(&SystemTaskLock{}).
		Where("task_id = ?", first.TaskID).
		Update("locked_until", common.GetTimestamp()-1).Error)

	require.NoError(t, ExpireStaleSystemTaskLocks(common.GetTimestamp()))

	reloadedFirst, err := GetSystemTaskByTaskID(first.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloadedFirst)
	assert.Equal(t, SystemTaskStatusFailed, reloadedFirst.Status)
	assert.Equal(t, "task lease expired", reloadedFirst.Error)
	assert.Nil(t, reloadedFirst.ActiveKey)

	var lockCount int64
	require.NoError(t, DB.Model(&SystemTaskLock{}).Where("task_id = ?", first.TaskID).Count(&lockCount).Error)
	assert.Equal(t, int64(0), lockCount)

	second, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)
	require.NotEqual(t, first.TaskID, second.TaskID)
}

func TestFindEarliestPendingSystemTasks(t *testing.T) {
	truncateTables(t)

	empty, err := FindEarliestPendingSystemTasks(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	firstA, err := CreateSystemTask("type_a", nil, nil)
	require.NoError(t, err)
	ignoredB, err := CreateSystemTask("type_b", nil, nil)
	require.NoError(t, err)
	_, claimed, err := ClaimSystemTask(ignoredB.ID, "type_b", "runner-b", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, FinishSystemTask(ignoredB.TaskID, "runner-b", SystemTaskStatusFailed, nil, "failed"))
	firstB, err := CreateSystemTask("type_b", nil, nil)
	require.NoError(t, err)
	ignoredC, err := CreateSystemTask("type_c", nil, nil)
	require.NoError(t, err)
	_, claimed, err = ClaimSystemTask(ignoredC.ID, "type_c", "runner-c", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, FinishSystemTask(ignoredC.TaskID, "runner-c", SystemTaskStatusFailed, nil, "failed"))

	tasks, err := FindEarliestPendingSystemTasks([]string{"type_a", "type_b", "type_c", "missing"})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.Equal(t, firstA.TaskID, tasks["type_a"].TaskID)
	assert.Equal(t, firstB.TaskID, tasks["type_b"].TaskID)
	assert.Nil(t, tasks["type_c"])
	assert.Nil(t, tasks["missing"])
}

func TestGetLatestSystemTask(t *testing.T) {
	truncateTables(t)

	latest, err := GetLatestSystemTask(SystemTaskTypeChannelTest)
	require.NoError(t, err)
	require.Nil(t, latest)

	first, err := CreateSystemTask(SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := ClaimSystemTask(first.ID, SystemTaskTypeChannelTest, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, FinishSystemTask(first.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))

	second, err := CreateSystemTask(SystemTaskTypeChannelTest, nil, nil)
	require.NoError(t, err)

	latest, err = GetLatestSystemTask(SystemTaskTypeChannelTest)
	require.NoError(t, err)
	require.NotNil(t, latest)
	assert.Equal(t, second.TaskID, latest.TaskID)
}

func TestGetLatestSystemTasks(t *testing.T) {
	truncateTables(t)

	empty, err := GetLatestSystemTasks(nil)
	require.NoError(t, err)
	assert.Empty(t, empty)

	firstA, err := CreateSystemTask("type_a", nil, nil)
	require.NoError(t, err)
	firstB, err := CreateSystemTask("type_b", nil, nil)
	require.NoError(t, err)
	_, claimed, err := ClaimSystemTask(firstA.ID, "type_a", "runner-a", common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)
	require.NoError(t, FinishSystemTask(firstA.TaskID, "runner-a", SystemTaskStatusSucceeded, nil, ""))
	secondA, err := CreateSystemTask("type_a", nil, nil)
	require.NoError(t, err)

	tasks, err := GetLatestSystemTasks([]string{"type_a", "type_b", "missing"})
	require.NoError(t, err)
	require.Len(t, tasks, 2)
	assert.NotEqual(t, firstA.TaskID, tasks["type_a"].TaskID)
	assert.Equal(t, secondA.TaskID, tasks["type_a"].TaskID)
	assert.Equal(t, firstB.TaskID, tasks["type_b"].TaskID)
	assert.Nil(t, tasks["missing"])
}

func TestRenewSystemTaskLock(t *testing.T) {
	truncateTables(t)

	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	newLockUntil := common.GetTimestamp() + 600
	require.NoError(t, RenewSystemTaskLock(task.TaskID, runnerID, newLockUntil))

	var lock SystemTaskLock
	require.NoError(t, DB.Where("task_id = ?", task.TaskID).First(&lock).Error)
	assert.Equal(t, newLockUntil, lock.LockedUntil)

	// A different runner cannot renew a lease it does not hold.
	assert.ErrorIs(t, RenewSystemTaskLock(task.TaskID, "runner-b", common.GetTimestamp()+600), ErrSystemTaskLockLost)

	// After the task finishes it is no longer running, so renew fails.
	require.NoError(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))
	assert.ErrorIs(t, RenewSystemTaskLock(task.TaskID, runnerID, common.GetTimestamp()+600), ErrSystemTaskLockLost)
}

func TestFinishSystemTaskRetainsExecutor(t *testing.T) {
	truncateTables(t)

	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "node-1-abc123"
	_, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""))

	reloaded, err := GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, SystemTaskStatusSucceeded, reloaded.Status)
	assert.Equal(t, runnerID, reloaded.LockedBy, "executor-of-record must be retained for history")

	var lockCount int64
	require.NoError(t, DB.Model(&SystemTaskLock{}).Where("task_id = ?", task.TaskID).Count(&lockCount).Error)
	assert.Equal(t, int64(0), lockCount)
}

func TestSystemTaskUpdatesRequireCurrentLock(t *testing.T) {
	truncateTables(t)

	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, DB.Model(&SystemTaskLock{}).
		Where("task_id = ?", task.TaskID).
		Updates(map[string]any{"locked_by": "runner-b"}).Error)

	assert.ErrorIs(t, UpdateSystemTaskState(task.TaskID, runnerID, testSystemTaskState{Progress: 10}), ErrSystemTaskLockLost)
	assert.ErrorIs(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""), ErrSystemTaskLockLost)
}

func TestSystemTaskUpdatesRequireUnexpiredLock(t *testing.T) {
	truncateTables(t)

	task, err := CreateSystemTask(SystemTaskTypeLogCleanup, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := ClaimSystemTask(task.ID, SystemTaskTypeLogCleanup, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	require.NoError(t, DB.Model(&SystemTaskLock{}).
		Where("task_id = ?", task.TaskID).
		Update("locked_until", common.GetTimestamp()-1).Error)

	assert.ErrorIs(t, UpdateSystemTaskState(task.TaskID, runnerID, testSystemTaskState{Progress: 10}), ErrSystemTaskLockLost)
	assert.ErrorIs(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, nil, ""), ErrSystemTaskLockLost)

	reloaded, err := GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, reloaded)
	assert.Equal(t, SystemTaskStatusRunning, reloaded.Status)
	assert.Empty(t, reloaded.State)
}

func TestUpdateSystemTaskStateIdenticalPayloadDoesNotLoseLock(t *testing.T) {
	// SQLite reports matched rows for unchanged UPDATEs, so this case passed
	// even before the fix. The MySQL regression is covered by
	// TestUpdateSystemTaskStateIdenticalPayloadDoesNotLoseLockConfiguredDatabases.
	truncateTables(t)
	runUpdateSystemTaskStateIdenticalPayloadKeepsLock(t, SystemTaskTypeLogCleanup)
}

func runUpdateSystemTaskStateIdenticalPayloadKeepsLock(t *testing.T, taskType string) {
	t.Helper()
	// Two persists in the same second so MySQL's unchanged-row UPDATE returns 0.

	task, err := CreateSystemTask(taskType, nil, nil)
	require.NoError(t, err)

	runnerID := "runner-a"
	_, claimed, err := ClaimSystemTask(task.ID, taskType, runnerID, common.GetTimestamp()+60)
	require.NoError(t, err)
	require.True(t, claimed)

	state := testSystemTaskState{Total: 10, Processed: 10, Progress: 100, Remaining: 0}
	require.NoError(t, UpdateSystemTaskState(task.TaskID, runnerID, state))
	require.NoError(t, UpdateSystemTaskState(task.TaskID, runnerID, state), "identical state persist must not be treated as lock loss")

	require.NoError(t, FinishSystemTask(task.TaskID, runnerID, SystemTaskStatusSucceeded, map[string]int64{"deleted_count": 10}, ""))
	finished, err := GetSystemTaskByTaskID(task.TaskID)
	require.NoError(t, err)
	require.NotNil(t, finished)
	assert.Equal(t, SystemTaskStatusSucceeded, finished.Status)
}

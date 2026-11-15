package model

import (
	"context"
	"fmt"
	"strconv"
	"time"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

type RankingQuotaTotal struct {
	ModelName   string `json:"model_name"`
	TotalTokens int64  `json:"total_tokens"`
}

type RankingQuotaBucket struct {
	ModelName string `json:"model_name"`
	Bucket    int64  `json:"bucket"`
	Tokens    int64  `json:"tokens"`
}

func GetRankingQuotaTotals(startTime int64, endTime int64) ([]RankingQuotaTotal, error) {
	return GetRankingQuotaTotalsWithContext(context.Background(), startTime, endTime)
}

func GetRankingQuotaTotalsWithContext(ctx context.Context, startTime int64, endTime int64) ([]RankingQuotaTotal, error) {
	var rows []RankingQuotaTotal
	err := withRankingQuotaQueryBudget(ctx, func(db *gorm.DB) error {
		query := db.Table("quota_data").
			Select("model_name, sum(token_used) as total_tokens").
			Where("model_name <> ''").
			Group("model_name").
			Having("sum(token_used) > 0").
			Order("total_tokens DESC, model_name ASC")
		return applyRankingQuotaTimeRange(query, startTime, endTime).Find(&rows).Error
	})
	return rows, err
}

func GetRankingQuotaBuckets(startTime int64, endTime int64, bucketSize int64) ([]RankingQuotaBucket, error) {
	return GetRankingQuotaBucketsWithContext(context.Background(), startTime, endTime, bucketSize)
}

// includeNonPositive retains signed bucket sums for deriving exact period totals.
// The default keeps the existing positive-only history query contract.
func GetRankingQuotaBucketsWithContext(ctx context.Context, startTime int64, endTime int64, bucketSize int64, includeNonPositive ...bool) ([]RankingQuotaBucket, error) {
	if bucketSize <= 0 {
		bucketSize = 3600
	}
	bucketExpr := rankingBucketExpr(bucketSize)
	var rows []RankingQuotaBucket
	err := withRankingQuotaQueryBudget(ctx, func(db *gorm.DB) error {
		query := db.Table("quota_data").
			Select(fmt.Sprintf("model_name, %s as bucket, sum(token_used) as tokens", bucketExpr)).
			Where("model_name <> ''").
			Group(fmt.Sprintf("model_name, %s", bucketExpr)).
			Order("bucket ASC, model_name ASC")
		if len(includeNonPositive) == 0 || !includeNonPositive[0] {
			query = query.Having("sum(token_used) > 0")
		}
		return applyRankingQuotaTimeRange(query, startTime, endTime).Find(&rows).Error
	})
	return rows, err
}

// Limits apply only to this SELECT/transaction, never to a pooled session.
func withRankingQuotaQueryBudget(ctx context.Context, fn func(*gorm.DB) error) error {
	return withRankingWork(ctx, func() error {
		ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
		defer cancel()
		if err := ctx.Err(); err != nil {
			return err
		}
		db := DB.WithContext(ctx)
		var err error
		switch db.Dialector.Name() {
		case "postgres":
			err = db.Transaction(func(tx *gorm.DB) error {
				deadline, _ := ctx.Deadline()
				ms := max(1, time.Until(deadline).Milliseconds())
				if err := tx.Exec("SET LOCAL statement_timeout = '" + strconv.FormatInt(ms, 10) + "ms'").Error; err != nil {
					return err
				}
				return fn(tx)
			})
		case "mysql":
			err = fn(db.Clauses(logExecutionHint{ctx}))
		default:
			err = fn(db)
		}
		return normalizeLogQueryError(ctx, err)
	})
}

func rankingBucketExpr(bucketSize int64) string {
	if common.UsingMainDatabase(common.DatabaseTypeMySQL) {
		return fmt.Sprintf("FLOOR(created_at / %d) * %d", bucketSize, bucketSize)
	}
	return fmt.Sprintf("(created_at / %d) * %d", bucketSize, bucketSize)
}

func applyRankingQuotaTimeRange(query *gorm.DB, startTime int64, endTime int64) *gorm.DB {
	if startTime > 0 {
		query = query.Where("created_at >= ?", startTime)
	}
	if endTime > 0 {
		query = query.Where("created_at <= ?", endTime)
	}
	return query
}

package model

import (
	"context"
	"crypto/hmac"
	"encoding/base64"
	"errors"
	"strconv"
	"strings"
	"time"

	"github.com/ClickHouse/clickhouse-go/v2"
	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/types"
	mysqldriver "github.com/go-sql-driver/mysql"
	"gorm.io/gorm"
	"gorm.io/gorm/clause"
)

const (
	LogQueryTimeout       = 5 * time.Second
	LogExportQueryTimeout = 30 * time.Second
	logCursorLifetime     = 15 * time.Minute
)

var ErrInvalidLogCursor = common.NewMessage("Invalid log cursor. Run the search again.")

// LogPage keeps the existing page response. Heavy searches report only rows
// already returned and never run COUNT over the JSON/text predicate.
type LogPage struct {
	Page       int    `json:"page"`
	PageSize   int    `json:"page_size"`
	Total      int64  `json:"total"`
	Items      []*Log `json:"items"`
	TotalExact bool   `json:"total_exact"`
	HasMore    bool   `json:"has_more"`
	NextCursor string `json:"next_cursor"`
}

type logCursor struct {
	Version   int    `json:"v"`
	Scope     string `json:"s"`
	CreatedAt int64  `json:"t"`
	ID        int    `json:"i"`
	RequestID string `json:"r,omitempty"`
	Seen      int    `json:"n"`
	Start     int64  `json:"a"`
	End       int64  `json:"b"`
	Expires   int64  `json:"e"`
}

// logExecutionHint is evaluated while each SELECT is built, so a later COUNT,
// page, or rate query receives only the budget remaining at that point.
type logExecutionHint struct{ context.Context }

func (hint logExecutionHint) Build(builder clause.Builder) {
	deadline, _ := hint.Deadline()
	ms := max(1, time.Until(deadline).Milliseconds())
	builder.WriteString("/*+ MAX_EXECUTION_TIME(" + strconv.FormatInt(ms, 10) + ") */")
}

func (hint logExecutionHint) ModifyStatement(statement *gorm.Statement) {
	selectClause := statement.Clauses["SELECT"]
	selectClause.AfterNameExpression = hint
	statement.Clauses["SELECT"] = selectClause
}

// withLogQueryBudget applies server-side limits without changing pooled
// sessions. PostgreSQL's setting lives only in this transaction; MySQL uses a
// SELECT hint; ClickHouse sends settings with this query's driver context.
func withLogQueryBudget(ctx context.Context, fn func(*gorm.DB) error) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	deadline, ok := ctx.Deadline()
	if !ok {
		return errors.New("log queries require a deadline")
	}
	db := LOG_DB.WithContext(ctx)
	var err error
	switch db.Dialector.Name() {
	case "postgres":
		err = db.Transaction(func(tx *gorm.DB) error {
			ms := max(1, time.Until(deadline).Milliseconds())
			if err := tx.Exec("SET LOCAL statement_timeout = '" + strconv.FormatInt(ms, 10) + "ms'").Error; err != nil {
				return err
			}
			return fn(tx)
		})
	case "mysql":
		err = fn(db.Clauses(logExecutionHint{ctx}))
	case "clickhouse":
		queryContext := clickhouse.Context(ctx, clickhouse.WithSettings(clickhouse.Settings{
			"max_execution_time":    max(0.001, time.Until(deadline).Seconds()),
			"timeout_overflow_mode": "throw",
		}))
		err = fn(db.WithContext(queryContext))
	default:
		err = fn(db)
	}
	return normalizeLogQueryError(ctx, err)
}

func normalizeLogQueryError(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return ctx.Err()
	}
	if err == nil {
		return nil
	}
	var mysqlError *mysqldriver.MySQLError
	var sqlState interface{ SQLState() string }
	var clickhouseError *clickhouse.Exception
	if (errors.As(err, &mysqlError) && mysqlError.Number == 3024) ||
		(errors.As(err, &sqlState) && sqlState.SQLState() == "57014") ||
		(errors.As(err, &clickhouseError) && (clickhouseError.Code == 159 || clickhouseError.Code == 160)) {
		return context.DeadlineExceeded
	}
	return err
}

func validateLogPagination(offset, limit int) error {
	if offset < 0 || limit < 1 || limit > 100 || offset > int(^uint(0)>>1)-limit {
		return common.NewMessage("Invalid log pagination")
	}
	return nil
}

func logQueryOrder(all bool) string {
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return clickHouseLogOrder("logs.")
	}
	if all {
		return "logs.created_at desc, logs.id desc"
	}
	return "logs.id desc"
}

func applyLogCursor(tx *gorm.DB, cursor logCursor, all bool) *gorm.DB {
	if all {
		return tx.Where("(logs.created_at < ? OR (logs.created_at = ? AND logs.id < ?))", cursor.CreatedAt, cursor.CreatedAt, cursor.ID)
	}
	return tx.Where("logs.id < ?", cursor.ID)
}

func logQueryScope(filter LogFilter, userID int, all bool, size int) (string, error) {
	payload, err := common.Marshal(struct {
		Filter   LogFilter
		UserID   int
		All      bool
		Size     int
		Database common.DatabaseType
	}{filter, userID, all, size, common.LogDatabaseType()})
	if err != nil {
		return "", err
	}
	return common.GenerateHMACWithKey([]byte("log-query-scope:"+common.CryptoSecret), string(payload)), nil
}

func decodeLogCursor(value, scope string) (logCursor, error) {
	var cursor logCursor
	if len(value) > 2048 {
		return cursor, ErrInvalidLogCursor
	}
	payload, signature, ok := strings.Cut(value, ".")
	mac := common.GenerateHMACWithKey([]byte("log-query-cursor:"+common.CryptoSecret), payload)
	if !ok || !hmac.Equal([]byte(signature), []byte(mac)) {
		return cursor, ErrInvalidLogCursor
	}
	data, err := base64.RawURLEncoding.DecodeString(payload)
	if err != nil || common.Unmarshal(data, &cursor) != nil || cursor.Version != 1 || cursor.Scope != scope ||
		cursor.Expires <= time.Now().Unix() || cursor.Seen <= 0 || cursor.CreatedAt < 0 || cursor.ID < 0 ||
		cursor.Start < 0 || cursor.End < cursor.Start || cursor.End-cursor.Start > 24*60*60 {
		return logCursor{}, ErrInvalidLogCursor
	}
	return cursor, nil
}

func encodeLogCursor(cursor logCursor) (string, error) {
	data, err := common.Marshal(cursor)
	if err != nil {
		return "", err
	}
	payload := base64.RawURLEncoding.EncodeToString(data)
	return payload + "." + common.GenerateHMACWithKey([]byte("log-query-cursor:"+common.CryptoSecret), payload), nil
}

// GetLogPage binds cursors to the caller, scope, page size and original filter.
// Every page reapplies the full filter and ownership boundary before seeking.
func GetLogPage(ctx context.Context, userID int, all bool, filter LogFilter, page, size int, cursorValue string) (result LogPage, err error) {
	if page < 1 || size < 1 || size > 100 || page > int(^uint(0)>>1)/size {
		return result, common.NewMessage("Invalid log pagination")
	}
	if err := filter.Validate(); err != nil {
		return result, err
	}
	result = LogPage{Page: page, PageSize: size, TotalExact: !filter.IsHeavy()}
	if !filter.IsHeavy() {
		if cursorValue != "" {
			return result, ErrInvalidLogCursor
		}
		result.Items, result.Total, err = getExactLogPage(ctx, userID, all, filter, (page-1)*size, size)
		result.HasMore = int64((page-1)*size)+int64(len(result.Items)) < result.Total
		return result, err
	}
	scope, err := logQueryScope(filter, userID, all, size)
	if err != nil {
		return result, err
	}
	bounded := filter
	cursor := logCursor{Version: 1, Scope: scope, Expires: time.Now().Add(logCursorLifetime).Unix()}
	if cursorValue != "" {
		cursor, err = decodeLogCursor(cursorValue, scope)
		if err != nil || cursor.Seen > int(^uint(0)>>1)-size || (page != 1 && (page-1)*size != cursor.Seen) {
			return result, ErrInvalidLogCursor
		}
		bounded.StartTimestamp, bounded.EndTimestamp = cursor.Start, cursor.End
		result.Page = cursor.Seen/size + 1
	} else if page != 1 {
		return result, ErrInvalidLogCursor
	} else {
		bounded, err = filter.LimitErrorSearchRange(time.Now().Unix())
		if err != nil {
			return result, err
		}
		cursor.Start, cursor.End = bounded.StartTimestamp, bounded.EndTimestamp
	}
	ctx, cancel := context.WithTimeout(ctx, LogQueryTimeout)
	defer cancel()
	err = withLogQueryBudget(ctx, func(db *gorm.DB) error {
		tx, err := bounded.apply(db.Model(&Log{}))
		if err != nil {
			return err
		}
		if !all {
			tx = tx.Where("logs.user_id = ?", userID)
		}
		if cursorValue != "" {
			if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
				// MergeTree logs have no unique key: retried rows may share both
				// ordering fields. A bounded OFFSET preserves those duplicate rows.
				tx = tx.Offset(cursor.Seen)
			} else {
				tx = applyLogCursor(tx, cursor, all)
			}
		}
		return tx.Order(logQueryOrder(all)).Limit(size + 1).Find(&result.Items).Error
	})
	if err != nil {
		return result, err
	}
	result.HasMore = len(result.Items) > size
	if result.HasMore {
		result.Items = result.Items[:size]
		last := result.Items[len(result.Items)-1]
		cursor.CreatedAt, cursor.ID, cursor.RequestID = last.CreatedAt, last.Id, last.RequestId
		cursor.Seen += len(result.Items)
		result.NextCursor, err = encodeLogCursor(cursor)
		if err != nil {
			return result, err
		}
	}
	// Capture database keys before formatUserLogs assigns display-only ids.
	seen := cursor.Seen
	if result.HasMore {
		seen -= len(result.Items)
	}
	result.Total = int64(seen) + int64(len(result.Items))
	if all {
		err = fillLogChannelNames(ctx, result.Items)
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			assignDisplayLogIds(result.Items, seen)
		}
	} else {
		formatUserLogs(result.Items, seen)
	}
	return result, normalizeLogQueryError(ctx, err)
}

func GetAllLogsWithContext(ctx context.Context, filter LogFilter, offset, limit int) ([]*Log, int64, error) {
	return getExactLogPage(ctx, 0, true, filter, offset, limit)
}

func GetUserLogsWithContext(ctx context.Context, userID int, filter LogFilter, offset, limit int) ([]*Log, int64, error) {
	return getExactLogPage(ctx, userID, false, filter, offset, limit)
}

func getExactLogPage(ctx context.Context, userID int, all bool, filter LogFilter, offset, limit int) (logs []*Log, total int64, err error) {
	if err := validateLogPagination(offset, limit); err != nil {
		return nil, 0, err
	}
	ctx, cancel := context.WithTimeout(ctx, LogQueryTimeout)
	defer cancel()
	err = withLogQueryBudget(ctx, func(db *gorm.DB) error {
		tx, err := filter.apply(db.Model(&Log{}))
		if err != nil {
			return err
		}
		if !all {
			tx = tx.Where("logs.user_id = ?", userID)
		}
		if err := tx.Session(&gorm.Session{}).Count(&total).Error; err != nil {
			return err
		}
		return tx.Order(logQueryOrder(all)).Limit(limit).Offset(offset).Find(&logs).Error
	})
	if err != nil {
		return nil, 0, err
	}
	if all {
		err = fillLogChannelNames(ctx, logs)
		if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
			assignDisplayLogIds(logs, offset)
		}
	} else {
		formatUserLogs(logs, offset)
	}
	return logs, total, normalizeLogQueryError(ctx, err)
}

func fillLogChannelNames(ctx context.Context, logs []*Log) error {
	channelIDs := types.NewSet[int]()
	for _, log := range logs {
		if log.ChannelId != 0 {
			channelIDs.Add(log.ChannelId)
		}
	}
	if channelIDs.Len() == 0 {
		return nil
	}
	channelNames := make(map[int]string)
	if common.MemoryCacheEnabled {
		for _, channelID := range channelIDs.Items() {
			if channel, err := CacheGetChannel(channelID); err == nil {
				channelNames[channelID] = channel.Name
			}
		}
	} else {
		var channels []struct {
			ID   int
			Name string
		}
		if err := DB.WithContext(ctx).Table("channels").Select("id, name").Where("id IN ?", channelIDs.Items()).Find(&channels).Error; err != nil {
			return err
		}
		for _, channel := range channels {
			channelNames[channel.ID] = channel.Name
		}
	}
	for _, log := range logs {
		log.ChannelName = channelNames[log.ChannelId]
	}
	return nil
}

func SumUsedQuotaWithContext(ctx context.Context, filter LogFilter) (Stat, error) {
	return sumLogQuotaWithContext(ctx, 0, true, filter)
}

func SumUserUsedQuotaWithContext(ctx context.Context, userID int, filter LogFilter) (Stat, error) {
	filter.Username = ""
	return sumLogQuotaWithContext(ctx, userID, false, filter)
}

func sumLogQuotaWithContext(ctx context.Context, userID int, all bool, filter LogFilter) (stat Stat, err error) {
	ctx, cancel := context.WithTimeout(ctx, LogQueryTimeout)
	defer cancel()
	err = withLogQueryBudget(ctx, func(db *gorm.DB) error {
		if !all {
			db = db.Where("logs.user_id = ?", userID)
		}
		var err error
		stat, err = sumUsedQuota(db, filter)
		return err
	})
	return stat, err
}

// Bounded batches release each SELECT/transaction before channel enrichment
// and socket writes, including when main and log databases share one pool.
func streamLogBatches(ctx context.Context, userID int, all bool, filter LogFilter, role int, fn func(*Log) error) error {
	ctx, cancel := context.WithTimeout(ctx, LogExportQueryTimeout)
	defer cancel()
	if common.UsingLogDatabase(common.DatabaseTypeClickHouse) {
		return streamClickHouseLogs(ctx, userID, all, filter, role, fn)
	}
	var cursor logCursor
	seen := 0
	for {
		var logs []*Log
		err := withLogQueryBudget(ctx, func(db *gorm.DB) error {
			tx, err := filter.apply(db.Model(&Log{}))
			if err != nil {
				return err
			}
			if !all {
				tx = tx.Where("logs.user_id = ?", userID)
			}
			if seen > 0 {
				tx = applyLogCursor(tx, cursor, all)
			}
			return tx.Order(logQueryOrder(all)).Limit(500).Find(&logs).Error
		})
		if err != nil {
			return err
		}
		if len(logs) == 0 {
			return nil
		}
		last := logs[len(logs)-1]
		cursor.CreatedAt, cursor.ID, cursor.RequestID = last.CreatedAt, last.Id, last.RequestId
		if all {
			if err := fillLogChannelNames(ctx, logs); err != nil {
				return normalizeLogQueryError(ctx, err)
			}
			if role >= common.RoleRootUser {
				FormatRootLogs(logs)
			} else {
				FormatAdminLogs(logs)
			}
		} else {
			formatUserLogs(logs, seen)
		}
		for _, log := range logs {
			if err := ctx.Err(); err != nil {
				return err
			}
			if err := fn(log); err != nil {
				return err
			}
		}
		seen += len(logs)
		if len(logs) < 500 {
			return ctx.Err()
		}
	}
}

// ClickHouse's non-unique (created_at, request_id) ordering cannot safely seek
// between batches. Its log database is separate from the relational main DB,
// so stream one bounded SELECT to retain every duplicate row.
func streamClickHouseLogs(ctx context.Context, userID int, all bool, filter LogFilter, role int, fn func(*Log) error) error {
	return withLogQueryBudget(ctx, func(db *gorm.DB) error {
		tx, err := filter.apply(db.Model(&Log{}))
		if err != nil {
			return err
		}
		if !all {
			tx = tx.Where("logs.user_id = ?", userID)
		}
		rows, err := tx.Order(clickHouseLogOrder("logs.")).Rows()
		if err != nil {
			return err
		}
		defer rows.Close()
		channelNames := make(map[int]string)
		seen := 0
		for rows.Next() {
			if err := ctx.Err(); err != nil {
				return err
			}
			var log Log
			if err := db.ScanRows(rows, &log); err != nil {
				return err
			}
			if all {
				if log.ChannelId != 0 {
					if _, ok := channelNames[log.ChannelId]; !ok {
						if err := fillLogChannelNames(ctx, []*Log{&log}); err != nil {
							return err
						}
						channelNames[log.ChannelId] = log.ChannelName
					}
					log.ChannelName = channelNames[log.ChannelId]
				}
				if role >= common.RoleRootUser {
					FormatRootLogs([]*Log{&log})
				} else {
					FormatAdminLogs([]*Log{&log})
				}
			} else {
				formatUserLogs([]*Log{&log}, seen)
			}
			seen++
			if err := fn(&log); err != nil {
				return err
			}
		}
		return rows.Err()
	})
}

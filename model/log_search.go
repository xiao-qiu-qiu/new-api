package model

import (
	"strings"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"gorm.io/gorm"
)

// Validate rejects unknown result values before exports start writing a response.
func (f LogFilter) Validate() error {
	for _, field := range []struct {
		value string
		limit int
	}{
		{f.ErrorKeyword, 256}, {f.ErrorCode, 128}, {f.RequestResult, 32},
		{f.ModelName, 256}, {f.Username, 256}, {f.TokenName, 256},
		{f.Group, 128}, {f.RequestId, 64}, {f.UpstreamRequestId, 128},
	} {
		if len(field.value) > field.limit || !utf8.ValidString(field.value) || strings.ContainsRune(field.value, 0) {
			return common.NewMessage("Invalid log search parameters")
		}
	}
	// Bound arithmetic as well as the timestamps passed to every dialect.
	const maxTimestamp = 253402300799 // 9999-12-31 UTC
	if f.Type < 0 || f.Type > LogTypeLogin || f.Channel < 0 ||
		f.StartTimestamp < 0 || f.EndTimestamp < 0 ||
		f.StartTimestamp > maxTimestamp || f.EndTimestamp > maxTimestamp ||
		(f.EndTimestamp != 0 && f.StartTimestamp > f.EndTimestamp) {
		return common.NewMessage("Invalid log search parameters")
	}
	switch f.RequestResult {
	case "", "success", "failed", "stream_error":
		return nil
	default:
		return common.NewMessage("Invalid request result")
	}
}

func (f LogFilter) IsHeavy() bool {
	return f.ErrorKeyword != "" || f.ErrorCode != "" || f.RequestResult != ""
}

// NeedsScanBudget concerns query cost, not permission. Name fragments and
// long historical COUNT/aggregations remain available to users but share the
// serial scan budget with administrator error searches and exports.
func (f LogFilter) NeedsScanBudget(now int64) bool {
	if f.IsHeavy() || f.ModelName != "" || f.TokenName != "" || f.Username != "" {
		return true
	}
	end := now
	if f.EndTimestamp != 0 {
		end = min(end, f.EndTimestamp)
	}
	return f.StartTimestamp == 0 || end-f.StartTimestamp > 24*60*60
}

// LimitErrorSearchRange bounds JSON/text scans even for administrators. The
// returned end also freezes a missing upper bound for subsequent cursor pages.
func (f LogFilter) LimitErrorSearchRange(now int64) (LogFilter, error) {
	if err := f.Validate(); err != nil {
		return f, err
	}
	if !f.IsHeavy() {
		return f, nil
	}
	end := now
	if f.EndTimestamp != 0 {
		end = min(end, f.EndTimestamp)
	}
	if f.StartTimestamp == 0 {
		f.StartTimestamp = max(0, end-24*60*60)
	}
	if f.StartTimestamp > end {
		return f, common.NewMessage("Invalid log search parameters")
	}
	if end-f.StartTimestamp > 24*60*60 {
		return f, common.NewMessage("Error log searches cannot exceed 24 hours")
	}
	f.EndTimestamp = end
	return f, nil
}

func logSearchJSON(source string) string {
	switch common.LogDatabaseType() {
	case common.DatabaseTypePostgreSQL:
		// Log writers store valid JSON. Historical empty/NULL metadata has no
		// error fields; arbitrary corrupt JSON is outside the storage contract.
		return "COALESCE(NULLIF(" + source + ", ''), '{}')::json"
	case common.DatabaseTypeMySQL:
		return "(CASE WHEN JSON_VALID(" + source + ") THEN " + source + " ELSE '{}' END)"
	case common.DatabaseTypeClickHouse:
		// ClickHouse's JSONExtract functions return defaults for malformed JSON.
		return "ifNull(" + source + ", '{}')"
	default:
		return "(CASE WHEN json_valid(" + source + ") THEN " + source + " ELSE '{}' END)"
	}
}

// Paths are fixed by this file, never supplied by the caller. Extracting only
// public error fields avoids searching hidden admin_info/root_info via self APIs.
func logSearchJSONText(source string, path ...string) string {
	var expression string
	switch common.LogDatabaseType() {
	case common.DatabaseTypePostgreSQL:
		expression = "(" + source + " #>> '{" + strings.Join(path, ",") + "}')"
	case common.DatabaseTypeMySQL:
		value := "JSON_EXTRACT(" + source + ", '$." + strings.Join(path, ".") + "')"
		expression = "CASE WHEN JSON_TYPE(" + value + ") = 'NULL' THEN '' ELSE JSON_UNQUOTE(" + value + ") END"
	case common.DatabaseTypeClickHouse:
		arguments := source + ", '" + strings.Join(path, "', '") + "'"
		expression = "multiIf(JSONType(" + arguments + ") = 'String', JSONExtractString(" + arguments + "), JSONType(" + arguments + ") = 'Null', '', JSONExtractRaw(" + arguments + "))"
	default:
		expression = "CAST(json_extract(" + source + ", '$." + strings.Join(path, ".") + "') AS TEXT)"
	}
	return "COALESCE(" + expression + ", '')"
}

func logSearchContains(expression string) string {
	switch common.LogDatabaseType() {
	case common.DatabaseTypeMySQL:
		return "LOCATE(LOWER(?), LOWER(" + expression + ")) > 0"
	case common.DatabaseTypePostgreSQL:
		return "POSITION(LOWER(?) IN LOWER(" + expression + ")) > 0"
	case common.DatabaseTypeClickHouse:
		return "positionCaseInsensitiveUTF8(" + expression + ", ?) > 0"
	default:
		return "INSTR(LOWER(" + expression + "), LOWER(?)) > 0"
	}
}

func (f LogFilter) applyErrorSearch(tx *gorm.DB) (*gorm.DB, error) {
	if err := f.Validate(); err != nil {
		return nil, err
	}
	if f.ErrorKeyword == "" && f.ErrorCode == "" && f.RequestResult == "" {
		return tx, nil
	}
	metadata := logSearchJSON("logs.other")

	if f.ErrorKeyword != "" {
		conditions := []string{logSearchContains("logs.content")}
		arguments := []any{f.ErrorKeyword}
		for _, path := range [][]string{
			{"error_code"}, {"error_type"}, {"status_code"}, {"error", "code"}, {"error", "type"}, {"error", "message"},
			{"stream_status", "status"}, {"stream_status", "response_status"}, {"stream_status", "end_reason"},
			{"stream_status", "end_error"}, {"stream_status", "error_code"}, {"stream_status", "error_type"},
		} {
			conditions = append(conditions, logSearchContains(logSearchJSONText(metadata, path...)))
			arguments = append(arguments, f.ErrorKeyword)
		}
		// Search decoded array entries so a literal backslash is not doubled by
		// JSON serialization. JSON_SEARCH is available on MySQL 5.7.8.
		switch common.LogDatabaseType() {
		case common.DatabaseTypeMySQL:
			pattern := strings.NewReplacer("!", "!!", "%", "!%", "_", "!_").Replace(strings.ToLower(f.ErrorKeyword))
			conditions = append(conditions, "JSON_SEARCH(LOWER("+metadata+"), 'one', ?, '!', '$.stream_status.errors[*]') IS NOT NULL")
			arguments = append(arguments, "%"+pattern+"%")
		case common.DatabaseTypePostgreSQL:
			errors := "(" + metadata + " #> '{stream_status,errors}')"
			conditions = append(conditions, "EXISTS (SELECT 1 FROM json_array_elements_text(CASE WHEN json_typeof("+errors+") = 'array' THEN "+errors+" ELSE '[]'::json END) log_error(message) WHERE "+logSearchContains("log_error.message")+")")
			arguments = append(arguments, f.ErrorKeyword)
		case common.DatabaseTypeClickHouse:
			conditions = append(conditions, "arrayExists(message -> positionCaseInsensitiveUTF8(message, ?) > 0, JSONExtract("+metadata+", 'stream_status', 'errors', 'Array(String)'))")
			arguments = append(arguments, f.ErrorKeyword)
		default:
			conditions = append(conditions, "EXISTS (SELECT 1 FROM json_each("+metadata+", '$.stream_status.errors') WHERE json_each.type = 'text' AND "+logSearchContains("json_each.value")+")")
			arguments = append(arguments, f.ErrorKeyword)
		}
		tx = tx.Where("("+strings.Join(conditions, " OR ")+")", arguments...)
	}

	if f.ErrorCode != "" {
		fields := []string{
			logSearchJSONText(metadata, "error_code"), logSearchJSONText(metadata, "error", "code"),
			logSearchJSONText(metadata, "stream_status", "error_code"), logSearchJSONText(metadata, "stream_status", "error", "code"),
		}
		if !common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
			terminal := logSearchJSON(logSearchJSONText(metadata, "stream_status", "end_error"))
			fields = append(fields, logSearchJSONText(terminal, "code"), logSearchJSONText(terminal, "error", "code"), logSearchJSONText(terminal, "response", "error", "code"))
		}
		conditions := make([]string, 0, len(fields)+1)
		arguments := make([]any, 0, len(fields)+1)
		for _, field := range fields {
			condition := field + " = ?"
			if common.UsingLogDatabase(common.DatabaseTypeMySQL) {
				condition = "CAST(" + field + " AS BINARY) = CAST(? AS BINARY)"
			}
			conditions = append(conditions, condition)
			arguments = append(arguments, f.ErrorCode)
		}
		if common.UsingLogDatabase(common.DatabaseTypePostgreSQL) {
			// A terminal can be plain text, so never cast end_error to json.
			// Historical provider JSON stores controlled error codes as quoted
			// code members. Match the complete value, including formatted JSON,
			// and ignore escaped mentions of "code" inside a message string.
			terminal := logSearchJSONText(metadata, "stream_status", "end_error")
			conditions = append(conditions, `EXISTS (SELECT 1 FROM regexp_matches(`+terminal+`, '(^|[{,])[[:space:]]*"code"[[:space:]]*:[[:space:]]*"([^"\\]*)"', 'g') AS log_error_code(captures) WHERE log_error_code.captures[2] = ?)`)
			arguments = append(arguments, f.ErrorCode)
		}
		tx = tx.Where("("+strings.Join(conditions, " OR ")+")", arguments...)
	}

	if f.RequestResult != "" {
		var hasErrors string
		switch common.LogDatabaseType() {
		case common.DatabaseTypeMySQL:
			errors := "JSON_EXTRACT(" + metadata + ", '$.stream_status.errors')"
			hasErrors = "(JSON_TYPE(" + errors + ") = 'ARRAY' AND JSON_LENGTH(" + errors + ") > 0)"
		case common.DatabaseTypePostgreSQL:
			errors := "(" + metadata + " #> '{stream_status,errors}')"
			hasErrors = "(CASE WHEN json_typeof(" + errors + ") = 'array' THEN json_array_length(" + errors + ") ELSE 0 END > 0)"
		case common.DatabaseTypeClickHouse:
			hasErrors = "length(JSONExtract(" + metadata + ", 'stream_status', 'errors', 'Array(String)')) > 0"
		default:
			hasErrors = "COALESCE(json_array_length(" + metadata + ", '$.stream_status.errors'), 0) > 0"
		}
		streamFailure := "(" + strings.Join([]string{
			logSearchJSONText(metadata, "stream_status", "status") + " = 'error'",
			logSearchJSONText(metadata, "stream_status", "response_status") + " IN ('failed', 'incomplete', 'cancelled', 'canceled')",
			logSearchJSONText(metadata, "stream_status", "end_reason") + " NOT IN ('', 'done', 'eof', 'handler_stop', 'null')",
			logSearchJSONText(metadata, "stream_status", "end_error") + " NOT IN ('', 'null')",
			logSearchJSONText(metadata, "stream_status", "error_count") + " NOT IN ('', '0', '0.0', 'null', 'false')",
			logSearchJSONText(metadata, "stream_status", "client_gone") + " IN ('true', '1')",
			"COALESCE(" + hasErrors + ", false)",
		}, " OR ") + ")"
		switch f.RequestResult {
		case "success":
			tx = tx.Where("logs.type = ? AND NOT "+streamFailure, LogTypeConsume)
		case "failed":
			tx = tx.Where("(logs.type = ? OR (logs.type = ? AND "+streamFailure+"))", LogTypeError, LogTypeConsume)
		case "stream_error":
			tx = tx.Where("logs.type = ? AND "+streamFailure, LogTypeConsume)
		}
	}
	return tx, nil
}

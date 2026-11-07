package active_requests

import (
	"container/heap"
	"context"
	"sort"
	"strings"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const MaxResults = 100

// Filter mirrors usage-log filters. A positive UserID always restricts results
// to that user; callers must obtain it from the authenticated server context.
type Filter struct {
	UserID            int
	Type              int
	StartTimestamp    int64 // Unix seconds, inclusive
	EndTimestamp      int64 // Unix seconds, inclusive
	ModelName         string
	Username          string
	TokenName         string
	Channel           int
	Group             string
	RequestID         string
	UpstreamRequestID string
}

type Result struct {
	Items      []Record `json:"items"`
	Total      int      `json:"total"`
	ServerTime int64    `json:"server_time"`
}

type cachedResult struct {
	result  Result
	err     error
	expires time.Time
}

func (t *tracker) query(ctx context.Context, filter Filter) (Result, error) {
	result := Result{Items: make([]Record, 0), ServerTime: time.Now().UnixMilli()}
	for _, pattern := range []string{filter.ModelName, filter.Username, filter.TokenName} {
		// Match model.validateLikePattern, including literal '_' and '!'. Plain
		// text is a substring search; '%' is the only supported wildcard.
		if strings.Contains(pattern, "%%") {
			return result, common.NewMessage("The search pattern cannot contain consecutive % wildcards")
		}
		count := strings.Count(pattern, "%")
		if count > 2 {
			return result, common.NewMessage("The search pattern can contain at most 2 % wildcards")
		}
		escaped := strings.NewReplacer("!", "!!", "_", "!_").Replace(pattern)
		if count > 0 && len(strings.ReplaceAll(escaped, "%", "")) < 2 {
			return result, common.NewMessage("Fuzzy search keywords must be at least 2 characters long")
		}
	}
	if (filter.Type != 0 && filter.Type != 2) || filter.UpstreamRequestID != "" || filter.UserID < 0 {
		return result, nil
	}
	var newest recordHeap
	if err := ctx.Err(); err != nil {
		return result, err
	}
	if t.shared && t.client == nil {
		return result, common.NewMessage("Failed to query logs")
	}
	if t.client != nil {
		remote, err := t.sharedResult(ctx, filter)
		if err != nil {
			return result, common.NewMessage("Failed to query logs")
		}
		result.Total = remote.Total
		for _, record := range remote.Items {
			newest.add(record)
		}
	}
	t.mu.RLock()
	var candidates map[string]struct{}
	switch {
	case filter.RequestID != "":
		candidates = t.requests[filter.RequestID]
	case filter.UserID > 0:
		candidates = t.users[filter.UserID]
	}
	if filter.RequestID != "" || filter.UserID > 0 {
		for id := range candidates {
			record := t.entries[id]
			if filter.matches(record) {
				result.Total++
				newest.add(record)
			}
		}
	} else {
		for _, record := range t.entries {
			if filter.matches(record) {
				result.Total++
				newest.add(record)
			}
		}
	}
	t.mu.RUnlock()
	result.Items = append(result.Items, newest...)
	sort.Slice(result.Items, func(i, j int) bool { return newer(result.Items[i], result.Items[j]) })
	return result, nil
}

func (f Filter) matches(record Record) bool {
	started := record.StartedAt / 1000
	return (f.UserID == 0 || record.UserID == f.UserID) &&
		(f.RequestID == "" || record.RequestID == f.RequestID) &&
		(f.Channel == 0 || record.Channel == f.Channel) &&
		(f.Group == "" || record.Group == f.Group) &&
		(f.StartTimestamp == 0 || started >= f.StartTimestamp) &&
		(f.EndTimestamp == 0 || started <= f.EndTimestamp) &&
		matchesName(record.ModelName, f.ModelName) &&
		matchesName(record.Username, f.Username) &&
		matchesName(record.TokenName, f.TokenName)
}

func matchesName(value, pattern string) bool {
	if pattern == "" {
		return true
	}
	switch common.LogDatabaseType() {
	case common.DatabaseTypeSQLite:
		// SQLite's default LIKE folds ASCII only, leaving e.g. É/é distinct.
		value, pattern = foldASCII(value), foldASCII(pattern)
	case common.DatabaseTypeMySQL:
		// The usual MySQL _ci collations ignore letter case. PostgreSQL and
		// ClickHouse LIKE remain case sensitive and must not be folded here.
		value, pattern = strings.ToLower(value), strings.ToLower(pattern)
	}
	if !strings.Contains(pattern, "%") {
		return strings.Contains(value, pattern)
	}
	// Validation limits this to three literal pieces, so matching is linear
	// and does not compile a regular expression for every row.
	parts := strings.Split(pattern, "%")
	remaining, ok := strings.CutPrefix(value, parts[0])
	if !ok {
		return false
	}
	for _, part := range parts[1 : len(parts)-1] {
		_, after, found := strings.Cut(remaining, part)
		if !found {
			return false
		}
		remaining = after
	}
	return strings.HasSuffix(remaining, parts[len(parts)-1])
}

func foldASCII(value string) string {
	return strings.Map(func(r rune) rune {
		if r >= 'A' && r <= 'Z' {
			return r + ('a' - 'A')
		}
		return r
	}, value)
}

func newer(a, b Record) bool {
	if a.StartedAt != b.StartedAt {
		return a.StartedAt > b.StartedAt
	}
	return a.RequestID > b.RequestID
}

// Only the newest 100 matches are retained while every match is counted.
type recordHeap []Record

func (h recordHeap) Len() int           { return len(h) }
func (h recordHeap) Less(i, j int) bool { return newer(h[j], h[i]) }
func (h recordHeap) Swap(i, j int)      { h[i], h[j] = h[j], h[i] }
func (h *recordHeap) Push(value any)    { *h = append(*h, value.(Record)) }
func (h *recordHeap) Pop() any {
	last := len(*h) - 1
	value := (*h)[last]
	*h = (*h)[:last]
	return value
}

func (h *recordHeap) add(record Record) {
	if len(*h) < MaxResults {
		heap.Push(h, record)
	} else if newer(record, (*h)[0]) {
		(*h)[0] = record
		heap.Fix(h, 0)
	}
}

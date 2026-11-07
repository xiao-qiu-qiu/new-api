// Package active_requests tracks live relay requests without owning their lifetime.
package active_requests

import (
	"context"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
	"github.com/gin-gonic/gin"
	"github.com/go-redis/redis/v8"
	"github.com/google/uuid"
	"golang.org/x/sync/singleflight"
)

const (
	StatusProcessing = "processing"
	StatusWaiting    = "waiting"
	StatusStreaming  = "streaming"
	StatusRetrying   = "retrying"
	StatusDraining   = "draining"

	contextKey = "active_request_handle"
	maxPending = 8192
)

type Record struct {
	RequestID   string `json:"request_id"`
	UserID      int    `json:"user_id"`
	Username    string `json:"username"`
	TokenID     int    `json:"token_id"`
	TokenName   string `json:"token_name"`
	ModelName   string `json:"model_name"`
	Group       string `json:"group"`
	Channel     int    `json:"channel"`
	ChannelName string `json:"channel_name"`
	StartedAt   int64  `json:"started_at"`
	UpdatedAt   int64  `json:"updated_at"`
	Status      string `json:"status"`
	IsStream    bool   `json:"is_stream"`
	Attempt     int    `json:"attempt"`
	ClientGone  bool   `json:"client_gone"`
}

// Handle is independent of Gin and request cancellation. The forwarding owner
// must call Finish only after all upstream work (including draining) has ended.
type Handle struct {
	tracker *tracker
	id      string
}

type change struct {
	record    Record
	remove    bool
	expiresAt int64
}

type tracker struct {
	mu       sync.RWMutex
	entries  map[string]Record
	users    map[int]map[string]struct{}
	requests map[string]map[string]struct{}
	pending  map[string]change
	nodeID   string
	client   *redis.Client
	shared   bool

	cacheMu sync.Mutex
	cache   map[Filter]cachedResult
	queries singleflight.Group
	readers chan struct{}
}

var defaultTracker struct {
	once  sync.Once
	value *tracker
}

func registry() *tracker {
	defaultTracker.once.Do(func() {
		var client *redis.Client
		if common.RedisEnabled {
			client = common.RDB
		}
		defaultTracker.value = newTracker(client)
		defaultTracker.value.shared = common.RedisEnabled
		if defaultTracker.value.client != nil {
			go defaultTracker.value.run()
		}
	})
	return defaultTracker.value
}

func newTracker(client *redis.Client) *tracker {
	if client != nil {
		// Observability has its own small connection pool and socket deadlines;
		// an unavailable Redis must not occupy the gateway's application pool.
		options := *client.Options()
		options.PoolSize, options.MinIdleConns = 4, 0
		options.MaxRetries = -1
		options.DialTimeout = redisTimeout
		options.ReadTimeout = redisTimeout
		options.WriteTimeout = redisTimeout
		options.PoolTimeout = redisTimeout
		client = redis.NewClient(&options)
	}
	return &tracker{
		entries: make(map[string]Record), users: make(map[int]map[string]struct{}),
		requests: make(map[string]map[string]struct{}), pending: make(map[string]change),
		nodeID: uuid.NewString() + ":", client: client, shared: client != nil,
		cache: make(map[Filter]cachedResult), readers: make(chan struct{}, 2),
	}
}

func Start(record Record) *Handle {
	return registry().start(record)
}

func (t *tracker) start(record Record) *Handle {
	if record.RequestID == "" || record.UserID <= 0 {
		return nil
	}
	now := time.Now().UnixMilli()
	if record.StartedAt <= 0 {
		record.StartedAt = now
	}
	record.UpdatedAt = now
	if !validStatus(record.Status) {
		record.Status = StatusProcessing
	}
	if record.ClientGone {
		record.Status = StatusDraining
	}
	record.Attempt = max(0, record.Attempt)
	h := &Handle{tracker: t, id: t.nodeID + uuid.NewString()}
	t.mu.Lock()
	t.entries[h.id] = record
	if t.users[record.UserID] == nil {
		t.users[record.UserID] = make(map[string]struct{})
	}
	t.users[record.UserID][h.id] = struct{}{}
	if t.requests[record.RequestID] == nil {
		t.requests[record.RequestID] = make(map[string]struct{})
	}
	t.requests[record.RequestID][h.id] = struct{}{}
	t.enqueue(h.id, change{record: record})
	t.mu.Unlock()
	return h
}

func validStatus(status string) bool {
	switch status {
	case StatusProcessing, StatusWaiting, StatusStreaming, StatusRetrying, StatusDraining:
		return true
	default:
		return false
	}
}

func (h *Handle) update(update func(*Record)) {
	if h == nil || h.tracker == nil {
		return
	}
	t := h.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	record, ok := t.entries[h.id]
	if !ok {
		return
	}
	update(&record)
	if record.ClientGone {
		record.Status = StatusDraining
	}
	record.UpdatedAt = time.Now().UnixMilli()
	t.entries[h.id] = record
	t.enqueue(h.id, change{record: record})
}

func (h *Handle) SetState(status string) {
	if validStatus(status) {
		h.update(func(record *Record) { record.Status = status })
	}
}

func (h *Handle) SetChannel(channel int, name, group string) {
	h.update(func(record *Record) {
		record.Channel, record.ChannelName, record.Group = channel, name, group
	})
}

func (h *Handle) SetStream(stream bool) {
	h.update(func(record *Record) { record.IsStream = stream })
}

func (h *Handle) SetAttempt(attempt int) {
	h.update(func(record *Record) { record.Attempt = max(0, attempt) })
}

func (h *Handle) MarkClientGone() {
	h.update(func(record *Record) { record.ClientGone = true })
}

func (h *Handle) Finish() {
	if h == nil || h.tracker == nil {
		return
	}
	t := h.tracker
	t.mu.Lock()
	defer t.mu.Unlock()
	record, ok := t.entries[h.id]
	if !ok {
		return
	}
	delete(t.entries, h.id)
	delete(t.users[record.UserID], h.id)
	if len(t.users[record.UserID]) == 0 {
		delete(t.users, record.UserID)
	}
	delete(t.requests[record.RequestID], h.id)
	if len(t.requests[record.RequestID]) == 0 {
		delete(t.requests, record.RequestID)
	}
	t.enqueue(h.id, change{record: record, remove: true})
}

// enqueue is called with mu held. Dropped updates are repaired by the next
// heartbeat; overflow deletions expire in Redis. Request owners never do I/O.
func (t *tracker) enqueue(id string, update change) {
	if t.client == nil {
		return
	}
	if _, exists := t.pending[id]; exists || len(t.pending) < maxPending {
		if update.remove && update.expiresAt == 0 {
			update.expiresAt = time.Now().Add(leaseTTL).UnixMilli()
		}
		t.pending[id] = update
	}
}

func FromContext(c *gin.Context) *Handle {
	if c == nil {
		return nil
	}
	value, _ := c.Get(contextKey)
	handle, _ := value.(*Handle)
	return handle
}

func SetContext(c *gin.Context, handle *Handle) {
	if c != nil {
		c.Set(contextKey, handle)
	}
}

// Query returns a bounded, newest-first view. Redis read failures are errors:
// a partial view must never look like other instances finished their requests.
func Query(ctx context.Context, filter Filter) (Result, error) {
	return registry().query(ctx, filter)
}

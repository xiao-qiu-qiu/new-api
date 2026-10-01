package service

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/QuantumNous/new-api/common"
	"github.com/QuantumNous/new-api/model"
)

type ChannelProbeView struct {
	model.ChannelProbe
	Latest        *model.ChannelProbeResult  `json:"latest"`
	History       []model.ChannelProbeResult `json:"history"`
	Status        string                     `json:"status"`
	Stale         bool                       `json:"stale"`
	ModelStatuses []PublicChannelProbeView   `json:"model_statuses"`
}

// Deliberately separate from the admin DTO: embedding configuration here
// would publish channel IDs and future internal configuration by accident.
type PublicChannelProbeView struct {
	PublicName   string                       `json:"public_name"`
	Scope        string                       `json:"scope"`
	WindowStart  int64                        `json:"window_start"`
	WindowEnd    int64                        `json:"window_end"`
	SampleCount  int64                        `json:"sample_count"`
	SuccessCount int64                        `json:"success_count"`
	Timeline     []ChannelProbeTimelineBucket `json:"timeline"`
	Model        string                       `json:"model"`
	GreenMS      int64                        `json:"green_ms"`
	YellowMS     int64                        `json:"yellow_ms"`
	Latest       *model.ChannelProbeResult    `json:"latest"`
	History      []model.ChannelProbeResult   `json:"history"`
	Status       string                       `json:"status"`
	Stale        bool                         `json:"stale"`
}

func ValidateChannelProbe(probe *model.ChannelProbe) error {
	probe.Model = strings.TrimSpace(probe.Model)
	probe.Normalize()
	probe.PublicName = strings.TrimSpace(probe.PublicName)
	probe.GroupName = strings.TrimSpace(probe.GroupName)
	if probe.Scope != "upstream" && probe.Scope != "group" {
		return errors.New("scope must be upstream or group")
	}
	if probe.Scope == "group" {
		if probe.GroupName == "" || utf8.RuneCountInString(probe.GroupName) > 64 || probe.GroupName == "auto" {
			return errors.New("an existing concrete group is required")
		}
		probe.ChannelID = 0
	} else {
		if probe.ChannelID <= 0 {
			return errors.New("channel_id is required")
		}
		probe.GroupName = ""
	}
	if len(probe.Models) < 1 || len(probe.Models) > 32 {
		return errors.New("select between 1 and 32 models")
	}
	seen := make(map[string]bool)
	for i, name := range probe.Models {
		name = strings.TrimSpace(name)
		if name == "" || utf8.RuneCountInString(name) > 191 || seen[name] {
			return errors.New("models must be unique nonempty names of at most 191 characters")
		}
		seen[name] = true
		probe.Models[i] = name
	}
	probe.Model = probe.Models[0]
	if probe.GreenMS == 0 && probe.YellowMS == 0 {
		probe.GreenMS, probe.YellowMS = 5000, 10000
	}
	if probe.SortOrder < 0 || probe.SortOrder > 1000000 {
		return errors.New("sort_order is out of range")
	}
	if utf8.RuneCountInString(probe.PublicName) > 128 || (probe.Public && probe.PublicName == "") {
		return errors.New("public probes require a display name of at most 128 characters")
	}
	if probe.Enabled && !probe.ActiveEnabled && !probe.PassiveEnabled {
		return errors.New("enable active or passive monitoring")
	}
	if probe.IntervalSeconds < 30 || probe.IntervalSeconds > 86400 {
		return errors.New("interval_seconds must be between 30 and 86400")
	}
	if probe.TimeoutSeconds < 5 || probe.TimeoutSeconds > 300 || probe.TimeoutSeconds > probe.IntervalSeconds {
		return errors.New("timeout_seconds must be between 5 and 300 and not exceed interval_seconds")
	}
	if probe.GreenMS < 1 || probe.YellowMS < probe.GreenMS || probe.YellowMS > 300000 {
		return errors.New("thresholds must satisfy 1 <= green_ms <= yellow_ms <= 300000")
	}
	return nil
}

func ChannelProbeStatus(success bool, ttft *int64, probe model.ChannelProbe) string {
	if !success {
		return "red"
	}
	if ttft == nil || *ttft < 0 {
		return "unknown"
	}
	if *ttft < probe.GreenMS {
		return "green"
	}
	if *ttft < probe.YellowMS {
		return "yellow"
	}
	return "red"
}

func ChannelProbeResultStatus(result model.ChannelProbeResult, probe model.ChannelProbe) string {
	if result.ErrorCode == "timeout" {
		return "timeout"
	}
	return ChannelProbeStatus(result.Success, result.TTFTMS, probe)
}

type ChannelProbeTimelineBucket struct {
	StartAt      int64  `json:"start_at"`
	EndAt        int64  `json:"end_at"`
	Count        int64  `json:"count"`
	SuccessCount int64  `json:"success_count"`
	TTFTMS       *int64 `json:"ttft_ms"`
	Status       string `json:"status"`
}

func BuildChannelProbeView(probe model.ChannelProbe, now time.Time) (ChannelProbeView, error) {
	probe.Normalize()
	view := ChannelProbeView{ChannelProbe: probe, History: make([]model.ChannelProbeResult, 0), Status: "unknown", Stale: true, ModelStatuses: make([]PublicChannelProbeView, 0, len(probe.Models))}
	for _, name := range probe.Models {
		item, err := BuildPublicChannelProbeModel(probe, name, now, 1)
		if err != nil {
			return view, err
		}
		view.ModelStatuses = append(view.ModelStatuses, item)
	}
	if len(view.ModelStatuses) > 0 {
		first := view.ModelStatuses[0]
		view.Latest, view.History, view.Status, view.Stale = first.Latest, first.History, first.Status, first.Stale
	}
	return view, nil
}

func BuildPublicChannelProbeModel(probe model.ChannelProbe, name string, now time.Time, hours int) (PublicChannelProbeView, error) {
	probe.Normalize()
	// All buckets contain whole minutes, including the current partial minute.
	// Window bounds are rounded up by at most 59 seconds; no samples are invented.
	end := (now.Unix()/60 + 1) * 60
	start := end - int64(hours)*3600
	view := PublicChannelProbeView{PublicName: probe.PublicName, Scope: probe.Scope, Model: name, GreenMS: probe.GreenMS, YellowMS: probe.YellowMS, WindowStart: start, WindowEnd: end, Status: "unknown", Stale: true, Timeline: make([]ChannelProbeTimelineBucket, 60)}
	history, err := model.GetChannelProbeHistory(probe.ID, name)
	if err != nil {
		return view, err
	}
	view.History = make([]model.ChannelProbeResult, 0, len(history))
	for _, r := range history {
		r.Status = ChannelProbeResultStatus(r, probe)
		if view.Latest == nil {
			latest := r
			view.Latest = &latest
			view.Stale = now.Unix()-r.CheckedAt > int64(max(3*probe.IntervalSeconds, probe.TimeoutSeconds+30))
			if probe.Enabled && !view.Stale {
				view.Status = r.Status
			}
		}
		if r.CheckedAt >= start && r.CheckedAt < end {
			view.History = append(view.History, r)
		}
	}
	minutes, err := model.GetChannelProbeMinutes(probe.ID, name, start, end)
	if err != nil {
		return view, err
	}
	sums := make([]int64, 60)
	counts := make([]int64, 60)
	timeouts := make([]int64, 60)
	width := int64(hours) * 60
	for i := range view.Timeline {
		view.Timeline[i] = ChannelProbeTimelineBucket{StartAt: start + int64(i)*width, EndAt: start + int64(i+1)*width, Status: "unknown"}
	}
	for _, m := range minutes {
		i := (m.MinuteAt - start) / width
		bucket := &view.Timeline[i]
		bucket.Count += m.Count
		bucket.SuccessCount += m.SuccessCount
		timeouts[i] += m.TimeoutCount
		sums[i] += m.TTFTSum
		counts[i] += m.TTFTCount
		view.SampleCount += m.Count
		view.SuccessCount += m.SuccessCount
	}
	for i := range view.Timeline {
		b := &view.Timeline[i]
		if counts[i] > 0 {
			ms := sums[i] / counts[i]
			b.TTFTMS = &ms
		}
		if b.Count == 0 {
			continue
		}
		b.Status = ChannelProbeStatus(b.SuccessCount == b.Count, b.TTFTMS, probe)
		if timeouts[i] > 0 {
			b.Status = "timeout"
		}
	}
	return view, nil
}

func PublicChannelProbe(view ChannelProbeView) PublicChannelProbeView {
	if len(view.ModelStatuses) > 0 {
		return view.ModelStatuses[0]
	}
	return PublicChannelProbeView{PublicName: view.PublicName, Scope: view.Scope, Model: view.Model, GreenMS: view.GreenMS, YellowMS: view.YellowMS, Latest: view.Latest, History: view.History, Status: view.Status, Stale: view.Stale}
}

var channelProbeRuntime = struct {
	sync.RWMutex
	probes []model.ChannelProbe
	runner func(context.Context, model.ChannelProbe) model.ChannelProbeResult
	active map[int]bool
	slots  chan struct{}
	once   sync.Once
}{active: make(map[int]bool), slots: make(chan struct{}, 4)}

func RefreshChannelProbeCache() error {
	probes, err := model.ListChannelProbeConfigs()
	if err != nil {
		return err
	}
	channelProbeRuntime.Lock()
	channelProbeRuntime.probes = probes
	channelProbeRuntime.Unlock()
	return nil
}

func PassiveChannelProbes(channelID int, modelName string) []model.ChannelProbe {
	channelProbeRuntime.RLock()
	defer channelProbeRuntime.RUnlock()
	var matches []model.ChannelProbe
	for _, probe := range channelProbeRuntime.probes {
		if probe.Enabled && probe.PassiveEnabled && probe.Scope != "group" && probe.ChannelID == channelID && slices.Contains(probe.Models, modelName) {
			probe.Model = modelName
			matches = append(matches, probe)
		}
	}
	return matches
}

func StartChannelProbeScheduler(runner func(context.Context, model.ChannelProbe) model.ChannelProbeResult) {
	channelProbeRuntime.once.Do(func() {
		channelProbeRuntime.Lock()
		channelProbeRuntime.runner = runner
		channelProbeRuntime.Unlock()
		go func() {
			ticker := time.NewTicker(5 * time.Second)
			defer ticker.Stop()
			lastCleanup := time.Time{}
			for {
				if err := RefreshChannelProbeCache(); err != nil {
					common.SysError("channel probe configuration refresh failed")
				} else if common.IsMasterNode {
					channelProbeRuntime.RLock()
					probes := append([]model.ChannelProbe(nil), channelProbeRuntime.probes...)
					channelProbeRuntime.RUnlock()
					for _, probe := range probes {
						if probe.Enabled && probe.ActiveEnabled && probe.NextRunAt <= time.Now().Unix() {
							_ = QueueChannelProbe(probe, false)
						}
					}
					if time.Since(lastCleanup) >= time.Hour {
						if err := model.CleanupChannelProbeResults(); err != nil {
							common.SysError("channel probe history cleanup failed")
						}
						lastCleanup = time.Now()
					}
				}
				<-ticker.C
			}
		}()
	})
}

func QueueChannelProbe(probe model.ChannelProbe, manual bool) error {
	if !common.IsMasterNode {
		return errors.New("active probes run on the master node")
	}
	if !probe.Enabled || !probe.ActiveEnabled {
		return errors.New("active monitoring is disabled")
	}
	channelProbeRuntime.Lock()
	runner := channelProbeRuntime.runner
	if runner == nil || channelProbeRuntime.active[probe.ID] {
		channelProbeRuntime.Unlock()
		return errors.New("probe is already running or scheduler is starting")
	}
	select {
	case channelProbeRuntime.slots <- struct{}{}:
		channelProbeRuntime.active[probe.ID] = true
	default:
		channelProbeRuntime.Unlock()
		return errors.New("probe concurrency limit reached")
	}
	channelProbeRuntime.Unlock()
	claimed, err := model.ClaimChannelProbe(&probe, manual)
	if err != nil || !claimed {
		channelProbeRuntime.Lock()
		delete(channelProbeRuntime.active, probe.ID)
		channelProbeRuntime.Unlock()
		<-channelProbeRuntime.slots
		return errors.New("probe is already running or configuration changed")
	}
	go func() {
		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		leaseDone := make(chan struct{})
		leaseStopped := make(chan struct{})
		go func() {
			defer close(leaseStopped)
			ticker := time.NewTicker(10 * time.Second)
			defer ticker.Stop()
			for {
				select {
				case <-leaseDone:
					return
				case <-ticker.C:
					owned, err := model.RenewChannelProbeLease(probe)
					if err != nil || !owned {
						cancel()
						return
					}
				}
			}
		}()
		defer func() {
			close(leaseDone)
			<-leaseStopped
			if err := model.ReleaseChannelProbe(probe); err != nil {
				common.SysError("channel probe lease release failed")
			}
			channelProbeRuntime.Lock()
			delete(channelProbeRuntime.active, probe.ID)
			channelProbeRuntime.Unlock()
			<-channelProbeRuntime.slots
		}()
		probe.Normalize()
		for _, name := range probe.Models {
			if ctx.Err() != nil {
				break
			}
			current, err := model.GetChannelProbe(probe.ID)
			if err != nil || current.Revision != probe.Revision {
				break
			}
			target := probe
			target.Model = name
			result := RunChannelProbeTarget(ctx, target, runner)
			if err := model.StoreChannelProbeResult(probe, result); err != nil {
				common.SysError("channel probe result persistence failed")
			}
		}
	}()
	return nil
}

// A model has its own deadline; one timeout does not starve the other models.
func RunChannelProbeTarget(parent context.Context, probe model.ChannelProbe, runner func(context.Context, model.ChannelProbe) model.ChannelProbeResult) (result model.ChannelProbeResult) {
	started := time.Now()
	ctx, cancel := context.WithTimeout(parent, time.Duration(probe.TimeoutSeconds)*time.Second)
	defer cancel()
	defer func() {
		if recover() != nil {
			result = model.ChannelProbeResult{ErrorCode: "internal_error"}
		}
		if errors.Is(ctx.Err(), context.DeadlineExceeded) {
			result.Success, result.ErrorCode = false, "timeout"
		}
		if errors.Is(ctx.Err(), context.Canceled) {
			result.Success, result.ErrorCode = false, "cancelled"
		}
		result.Model, result.Source, result.CheckedAt, result.LatencyMS = probe.Model, "active", time.Now().Unix(), time.Since(started).Milliseconds()
		result.Status = ChannelProbeResultStatus(result, probe)
	}()
	return runner(ctx, probe)
}

func PassiveGroupProbes(group, modelName string) []model.ChannelProbe {
	channelProbeRuntime.RLock()
	defer channelProbeRuntime.RUnlock()
	var matches []model.ChannelProbe
	for _, probe := range channelProbeRuntime.probes {
		if probe.Enabled && probe.PassiveEnabled && probe.Scope == "group" && (group == "" || probe.GroupName == group) && slices.Contains(probe.Models, modelName) {
			probe.Model = modelName
			matches = append(matches, probe)
		}
	}
	return matches
}

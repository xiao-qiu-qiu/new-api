package service

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/QuantumNous/new-api/common"
)

const (
	rankingBackgroundTimeout = 10 * time.Minute
	rankingSnapshotMaxStale  = 15 * time.Minute
	rankingBuildRetryDelay   = 15 * time.Second
)

var ErrRankingsPending = errors.New("rankings snapshot is being prepared")

type rankingBuild struct {
	done     chan struct{}
	cancel   context.CancelFunc
	err      error
	finished time.Time
}

var rankingBuilds = struct {
	sync.Mutex
	items map[string]*rankingBuild
}{items: make(map[string]*rankingBuild)}

// A browser request waits only briefly. The shared build survives tab changes,
// while a bounded job count and the model's paced scan batches bound the work.
func runRankingBuild(ctx context.Context, key string, build func(context.Context) error, waitBudget ...time.Duration) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	now := time.Now()
	rankingBuilds.Lock()
	active := 0
	for name, job := range rankingBuilds.items {
		if job.finished.IsZero() {
			active++
			continue
		}
		if job.err == nil || errors.Is(job.err, context.Canceled) || now.Sub(job.finished) >= rankingBuildRetryDelay {
			delete(rankingBuilds.items, name)
		}
	}
	job := rankingBuilds.items[key]
	if job == nil {
		if active >= 16 {
			rankingBuilds.Unlock()
			return ErrRankingsPending
		}
		buildCtx, cancel := context.WithTimeout(context.Background(), rankingBackgroundTimeout)
		job = &rankingBuild{done: make(chan struct{}), cancel: cancel}
		rankingBuilds.items[key] = job
		go func() {
			var buildErr error
			defer func() {
				if failure := recover(); failure != nil {
					buildErr = fmt.Errorf("rankings build panic: %v", failure)
				}
				cancel()
				rankingBuilds.Lock()
				job.err, job.finished = buildErr, time.Now()
				close(job.done)
				rankingBuilds.Unlock()
				if buildErr != nil && !errors.Is(buildErr, context.Canceled) {
					common.SysError(common.LogText("rankings background build failed: %s", buildErr.Error()))
				}
			}()
			buildErr = build(buildCtx)
		}()
	}
	rankingBuilds.Unlock()

	wait := 150 * time.Millisecond
	if len(waitBudget) > 0 {
		wait = waitBudget[0]
	}
	if wait <= 0 {
		select {
		case <-job.done:
			return job.err
		default:
			return ErrRankingsPending
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-job.done:
		return job.err
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return ErrRankingsPending
	}
}

func cancelUserRankingBuilds(generation int) {
	rankingBuilds.Lock()
	defer rankingBuilds.Unlock()
	prefix := fmt.Sprintf("users:%d:", generation)
	for key, job := range rankingBuilds.items {
		if strings.HasPrefix(key, prefix) {
			job.cancel()
		}
	}
}

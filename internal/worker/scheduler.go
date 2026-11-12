package worker

import (
	"context"
	"fmt"
	"log"
	"strings"
	"sync"
	"time"

	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"t-invest-portfolio/internal/portfolio"
)

var ErrDependencyNotReady = fmt.Errorf("ожидание начальных данных портфеля")

type Task struct {
	Name         string
	Method       string
	Interval     time.Duration
	InitialDelay time.Duration
	Run          func(context.Context) error
}

type Scheduler struct {
	Tasks    []Task
	Logger   *log.Logger
	OnResult func(context.Context, Task, error)
}

func (s *Scheduler) Run(ctx context.Context) error {
	var wg sync.WaitGroup
	for _, task := range s.Tasks {
		task := task
		if task.Interval <= 0 || task.Run == nil {
			return fmt.Errorf("invalid task %q", task.Name)
		}
		wg.Add(1)
		go func() {
			defer wg.Done()
			backoff := time.Second
			if task.InitialDelay > 0 && !wait(ctx, task.InitialDelay) {
				return
			}
			for ctx.Err() == nil {
				err := task.Run(ctx)
				if err != ErrDependencyNotReady && s.OnResult != nil && ctx.Err() == nil {
					s.OnResult(ctx, task, err)
				}
				if err != nil && ctx.Err() == nil {
					if err != ErrDependencyNotReady && s.Logger != nil {
						s.Logger.Printf("задача %s: %v", task.Name, err)
					}
					if err == ErrDependencyNotReady {
						wait(ctx, 30*time.Second)
						continue
					}
					if isRateError(err) {
						wait(ctx, backoff)
						backoff = min(backoff*2, 5*time.Minute)
						continue
					}
				} else {
					backoff = time.Second
				}
				if !wait(ctx, task.Interval) {
					return
				}
			}
		}()
	}
	<-ctx.Done()
	wg.Wait()
	return nil
}

func wait(ctx context.Context, duration time.Duration) bool {
	timer := time.NewTimer(duration)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

type quotaSlot struct {
	interval time.Duration
	next     time.Time
	mu       sync.Mutex
}

type QuotaGate struct {
	mu       sync.RWMutex
	methods  map[string][]*quotaSlot
	fallback map[string]*quotaSlot
	global   *quotaSlot
}

func NewQuotaGate(profile portfolio.Profile) *QuotaGate {
	g := &QuotaGate{methods: make(map[string][]*quotaSlot), fallback: make(map[string]*quotaSlot), global: &quotaSlot{interval: 25 * time.Millisecond}}
	g.Update(profile)
	return g
}

func (g *QuotaGate) Update(profile portfolio.Profile) {
	methods := make(map[string][]*quotaSlot)
	for _, limit := range profile.Limits {
		if limit.Kind != "unary" {
			continue
		}
		interval := time.Duration(0)
		if limit.PerSecond > 0 {
			interval = time.Second / time.Duration(limit.PerSecond)
		}
		if limit.PerMinute > 0 {
			perMinute := time.Minute / time.Duration(limit.PerMinute)
			if perMinute > interval {
				interval = perMinute
			}
		}
		if interval <= 0 {
			continue
		}
		// Keep 20% headroom below the broker's advertised quota.
		interval += interval / 4
		slot := &quotaSlot{interval: interval}
		for _, method := range limit.Methods {
			name := methodName(method)
			if name != "" {
				methods[name] = append(methods[name], slot)
			}
		}
	}
	g.mu.Lock()
	g.methods = methods
	g.mu.Unlock()
}

func (g *QuotaGate) Wait(ctx context.Context, method string) error {
	g.mu.RLock()
	slots := append([]*quotaSlot(nil), g.methods[methodName(method)]...)
	g.mu.RUnlock()
	if len(slots) == 0 {
		name := methodName(method)
		g.mu.Lock()
		slot := g.fallback[name]
		if slot == nil {
			slot = &quotaSlot{interval: 5 * time.Second}
			g.fallback[name] = slot
		}
		g.mu.Unlock()
		slots = []*quotaSlot{slot}
	}
	for _, slot := range append(slots, g.global) {
		if err := slot.wait(ctx); err != nil {
			return err
		}
	}
	return nil
}

func (slot *quotaSlot) wait(ctx context.Context) error {
	slot.mu.Lock()
	now := time.Now()
	start := now
	if slot.next.After(start) {
		start = slot.next
	}
	slot.next = start.Add(slot.interval)
	slot.mu.Unlock()
	if delay := time.Until(start); delay > 0 && !wait(ctx, delay) {
		return ctx.Err()
	}
	return ctx.Err()
}

func methodName(value string) string {
	value = strings.TrimSpace(value)
	value = strings.Trim(value, "/")
	if index := strings.LastIndexAny(value, "/."); index >= 0 {
		value = value[index+1:]
	}
	return value
}

func isRateError(err error) bool {
	if err == nil {
		return false
	}
	if status.Code(err) == codes.ResourceExhausted {
		return true
	}
	message := strings.ToLower(err.Error())
	return strings.Contains(message, "429") || strings.Contains(message, "resource_exhausted") || strings.Contains(message, "resourceexhausted") || strings.Contains(message, "rate limit") || strings.Contains(message, "лимит")
}

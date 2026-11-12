package worker

import (
	"bytes"
	"context"
	"encoding/json"
	"log"
	"sync/atomic"
	"testing"
	"time"

	"t-invest-portfolio/internal/portfolio"
)

func TestQuotaGateUsesMethodGroupAndGlobalLimit(t *testing.T) {
	gate := NewQuotaGate(portfolio.Profile{Limits: []portfolio.RateLimit{{
		Kind: "unary", PerMinute: 60, PerSecond: 2, Methods: []string{"OperationsService/GetPortfolio"},
	}}})
	if err := gate.Wait(context.Background(), "GetPortfolio"); err != nil {
		t.Fatal(err)
	}
	started := time.Now()
	if err := gate.Wait(context.Background(), "GetPortfolio"); err != nil {
		t.Fatal(err)
	}
	if elapsed := time.Since(started); elapsed < 575*time.Millisecond {
		t.Fatalf("second call started too early: %v", elapsed)
	}
	if err := gate.Wait(context.Background(), "GetPositions"); err != nil {
		t.Fatalf("method without advertised quota should use conservative fallback: %v", err)
	}
}

func TestQuotaGateUsesConservativeFallbackForUnadvertisedMethod(t *testing.T) {
	gate := NewQuotaGate(portfolio.Profile{})
	if err := gate.Wait(context.Background(), "InstrumentByUid"); err != nil {
		t.Fatal(err)
	}

	gate.mu.RLock()
	slot := gate.fallback["InstrumentByUid"]
	gate.mu.RUnlock()
	if slot == nil {
		t.Fatal("fallback quota was not created")
	}
	if slot.interval != 5*time.Second {
		t.Fatalf("fallback interval = %s, want 5s", slot.interval)
	}
}

func TestSchedulerTasksAreIndependentAndNeverOverlap(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	var healthy, active, maximum atomic.Int32
	var output bytes.Buffer
	scheduler := Scheduler{Logger: log.New(&output, "", 0), Tasks: []Task{
		{Name: "failing", Interval: 5 * time.Millisecond, Run: func(context.Context) error { return context.DeadlineExceeded }},
		{Name: "healthy", Interval: 5 * time.Millisecond, Run: func(context.Context) error {
			current := active.Add(1)
			for {
				old := maximum.Load()
				if current <= old || maximum.CompareAndSwap(old, current) {
					break
				}
			}
			healthy.Add(1)
			time.Sleep(12 * time.Millisecond)
			active.Add(-1)
			return nil
		}},
	}}
	done := make(chan error, 1)
	go func() { done <- scheduler.Run(ctx) }()
	time.Sleep(65 * time.Millisecond)
	cancel()
	if err := <-done; err != nil {
		t.Fatal(err)
	}
	if healthy.Load() < 2 {
		t.Fatalf("healthy task ran %d times", healthy.Load())
	}
	if maximum.Load() != 1 {
		t.Fatalf("same task overlapped, max concurrent=%d", maximum.Load())
	}
	if output.Len() == 0 {
		t.Fatal("task failure was not logged")
	}
}

func TestOperationKeyIsStableAndAccountScoped(t *testing.T) {
	when := time.Date(2026, 11, 12, 12, 0, 0, 0, time.FixedZone("local", 5*60*60))
	op := portfolio.Operation{At: when, Type: "BUY", InstrumentUID: "uid-1", Quantity: 2, Payment: portfolio.Money{Currency: "RUB"}}
	first := operationKey("account-a", op)
	if first != operationKey("account-a", op) {
		t.Fatal("same operation produced different key")
	}
	if first == operationKey("account-b", op) {
		t.Fatal("same event on another account must have a different key")
	}
	if first == "" || len(first) != 64 {
		t.Fatalf("key = %q", first)
	}
}

func TestOperationHistoryStartsAtUnixEpoch(t *testing.T) {
	got, err := operationFrom(nil)
	if err != nil {
		t.Fatal(err)
	}
	want := time.Date(1970, time.January, 1, 0, 0, 0, 0, time.UTC)
	if !got.Equal(want) {
		t.Fatalf("initial operation history start = %s, want %s", got, want)
	}
}

func TestOperationFromWatermarkUsesOneDayOverlap(t *testing.T) {
	last := time.Date(2026, time.November, 12, 12, 0, 0, 0, time.UTC)
	payload, err := json.Marshal(last)
	if err != nil {
		t.Fatal(err)
	}
	got, err := operationFrom(payload)
	if err != nil {
		t.Fatal(err)
	}
	want := last.Add(-24 * time.Hour)
	if !got.Equal(want) {
		t.Fatalf("incremental operation start = %s, want %s", got, want)
	}
}

func TestOperationFromRejectsCorruptWatermark(t *testing.T) {
	if _, err := operationFrom([]byte("not-a-time")); err == nil {
		t.Fatal("expected corrupt watermark to fail")
	}
}

func TestOperationNextCursor(t *testing.T) {
	tests := []struct {
		name                 string
		hasNext              bool
		next, current        string
		wantNext             string
		wantContinue, wantOK bool
	}{
		{name: "finished", hasNext: false, wantOK: true},
		{name: "next page", hasNext: true, next: "cursor-2", current: "cursor-1", wantNext: "cursor-2", wantContinue: true, wantOK: true},
		{name: "empty cursor", hasNext: true, current: "cursor-1", wantOK: false},
		{name: "repeated cursor", hasNext: true, next: "cursor-1", current: "cursor-1", wantOK: false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			gotNext, gotContinue, gotOK := operationNextCursor(test.hasNext, test.next, test.current)
			if gotNext != test.wantNext || gotContinue != test.wantContinue || gotOK != test.wantOK {
				t.Fatalf("operationNextCursor() = (%q, %t, %t), want (%q, %t, %t)", gotNext, gotContinue, gotOK, test.wantNext, test.wantContinue, test.wantOK)
			}
		})
	}
}

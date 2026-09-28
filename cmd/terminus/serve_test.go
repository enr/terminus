package main

import (
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"github.com/enr/terminus/internal/model"
)

func TestCachedCollectorReusesWithinTTL(t *testing.T) {
	var calls atomic.Int32
	c := &cachedCollector{
		ttl: time.Hour,
		collect: func(context.Context, bool) (*model.Report, error) {
			calls.Add(1)
			return model.NewReport(), nil
		},
	}
	for range 5 {
		if _, err := c.get(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if n := calls.Load(); n != 1 {
		t.Fatalf("collect called %d times, want 1", n)
	}
}

func TestCachedCollectorRefreshesAfterTTL(t *testing.T) {
	var calls atomic.Int32
	c := &cachedCollector{
		ttl: time.Millisecond,
		collect: func(context.Context, bool) (*model.Report, error) {
			calls.Add(1)
			return model.NewReport(), nil
		},
	}
	c.get(context.Background())
	time.Sleep(5 * time.Millisecond)
	c.get(context.Background())
	if n := calls.Load(); n != 2 {
		t.Fatalf("collect called %d times, want 2", n)
	}
}

func TestCachedCollectorDisabled(t *testing.T) {
	var calls atomic.Int32
	c := &cachedCollector{
		ttl: 0,
		collect: func(context.Context, bool) (*model.Report, error) {
			calls.Add(1)
			return model.NewReport(), nil
		},
	}
	for range 3 {
		c.get(context.Background())
	}
	if n := calls.Load(); n != 3 {
		t.Fatalf("collect called %d times, want 3 (cache disabled)", n)
	}
}

func TestCachedCollectorPropagatesError(t *testing.T) {
	want := errors.New("boom")
	c := &cachedCollector{
		ttl: time.Hour,
		collect: func(context.Context, bool) (*model.Report, error) {
			return nil, want
		},
	}
	if _, err := c.get(context.Background()); !errors.Is(err, want) {
		t.Fatalf("err = %v, want %v", err, want)
	}
}

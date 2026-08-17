package httpapi

import (
	"sync"
	"time"
)

type limitState struct {
	count int
	reset time.Time
}

type fixedWindowLimiter struct {
	mu         sync.Mutex
	limit      int
	window     time.Duration
	now        func() time.Time
	states     map[string]limitState
	maxEntries int
}

type limitResult struct {
	Allowed    bool
	Limit      int
	Remaining  int
	Reset      time.Time
	RetryAfter time.Duration
}

func newFixedWindowLimiter(limit int, window time.Duration) *fixedWindowLimiter {
	return &fixedWindowLimiter{
		limit: limit, window: window, now: time.Now,
		states: make(map[string]limitState), maxEntries: 100_000,
	}
}

func (l *fixedWindowLimiter) Take(key string) limitResult {
	l.mu.Lock()
	defer l.mu.Unlock()

	now := l.now()
	state, ok := l.states[key]
	if !ok && len(l.states) >= l.maxEntries {
		for existingKey, existingState := range l.states {
			if !now.Before(existingState.reset) {
				delete(l.states, existingKey)
			}
		}
		if len(l.states) >= l.maxEntries {
			return limitResult{
				Allowed: false, Limit: l.limit, Remaining: 0,
				Reset: now.Add(l.window), RetryAfter: l.window,
			}
		}
	}
	if !ok || !now.Before(state.reset) {
		state = limitState{reset: now.Add(l.window)}
	}
	if state.count >= l.limit {
		l.states[key] = state
		return limitResult{Allowed: false, Limit: l.limit, Remaining: 0, Reset: state.reset, RetryAfter: state.reset.Sub(now)}
	}
	state.count++
	l.states[key] = state
	remaining := l.limit - state.count
	return limitResult{Allowed: true, Limit: l.limit, Remaining: remaining, Reset: state.reset}
}

func (l *fixedWindowLimiter) Reset(key string) {
	l.mu.Lock()
	delete(l.states, key)
	l.mu.Unlock()
}

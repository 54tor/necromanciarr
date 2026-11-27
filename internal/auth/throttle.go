package auth

import (
	"sync"
	"time"
)

const (
	ThrottleBaseDelay = time.Second
	ThrottleMaxDelay  = time.Minute

	throttleStaleAfter = 2 * ThrottleMaxDelay
	maxDelayShift      = 30
)

type throttleState struct {
	consecutiveFailures int
	nextAllowedAt       time.Time
}

type Throttle struct {
	mutex     sync.Mutex
	now       func() time.Time
	states    map[string]throttleState
	lastSweep time.Time
}

func NewThrottle(now func() time.Time) *Throttle {
	if now == nil {
		now = time.Now
	}
	return &Throttle{now: now, states: make(map[string]throttleState)}
}

func (throttle *Throttle) Allow(key string) (retryAfter time.Duration, allowed bool) {
	throttle.mutex.Lock()
	defer throttle.mutex.Unlock()
	current := throttle.now()
	throttle.evictStale(current)
	nextAllowedAt := throttle.states[key].nextAllowedAt
	if !current.Before(nextAllowedAt) {
		return 0, true
	}
	return nextAllowedAt.Sub(current), false
}

func (throttle *Throttle) RecordFailure(key string) {
	throttle.mutex.Lock()
	defer throttle.mutex.Unlock()
	current := throttle.now()
	throttle.evictStale(current)
	failures := throttle.states[key].consecutiveFailures + 1
	throttle.states[key] = throttleState{consecutiveFailures: failures, nextAllowedAt: current.Add(delayAfter(failures))}
}

func (throttle *Throttle) RecordSuccess(key string) {
	throttle.mutex.Lock()
	defer throttle.mutex.Unlock()
	delete(throttle.states, key)
}

func delayAfter(failures int) time.Duration {
	shift := min(failures-1, maxDelayShift)
	return min(ThrottleBaseDelay<<shift, ThrottleMaxDelay)
}

func (throttle *Throttle) evictStale(current time.Time) {
	if current.Sub(throttle.lastSweep) < ThrottleMaxDelay {
		return
	}
	throttle.lastSweep = current
	for key, state := range throttle.states {
		if current.Sub(state.nextAllowedAt) > throttleStaleAfter {
			delete(throttle.states, key)
		}
	}
}

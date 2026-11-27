package auth

import (
	"sync"
	"time"
)

const (
	ThrottleBaseDelay = time.Second
	ThrottleMaxDelay  = time.Minute

	throttleStaleAfter     = 2 * ThrottleMaxDelay
	throttleReservationTTL = 2 * time.Minute
	maxDelayShift          = 30
)

type throttleState struct {
	consecutiveFailures int
	nextAllowedAt       time.Time
	inFlightSince       time.Time
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

// Allow admits an attempt and reserves the key in one atomic step. Every admitted
// attempt MUST end with exactly one of RecordFailure, RecordSuccess or Cancel.
// While an attempt is in flight, other attempts are refused with at least
// ThrottleBaseDelay as retryAfter; refused attempts never count as failures.
func (throttle *Throttle) Allow(key string) (retryAfter time.Duration, allowed bool) {
	throttle.mutex.Lock()
	defer throttle.mutex.Unlock()
	current := throttle.prepare()
	state := throttle.states[key]
	remaining := state.nextAllowedAt.Sub(current)
	if reservationActive(state, current) {
		return max(remaining, ThrottleBaseDelay), false
	}
	if remaining > 0 {
		return remaining, false
	}
	state.inFlightSince = current
	throttle.states[key] = state
	return 0, true
}

func (throttle *Throttle) RecordFailure(key string) {
	throttle.mutex.Lock()
	defer throttle.mutex.Unlock()
	current := throttle.prepare()
	failures := throttle.states[key].consecutiveFailures + 1
	throttle.states[key] = throttleState{consecutiveFailures: failures, nextAllowedAt: current.Add(delayAfter(failures))}
}

// Cancel releases the reservation of an attempt that ended without a verdict.
func (throttle *Throttle) Cancel(key string) {
	throttle.mutex.Lock()
	defer throttle.mutex.Unlock()
	throttle.prepare()
	state, found := throttle.states[key]
	if !found {
		return
	}
	state.inFlightSince = time.Time{}
	if state.consecutiveFailures == 0 {
		delete(throttle.states, key)
		return
	}
	throttle.states[key] = state
}

func reservationActive(state throttleState, current time.Time) bool {
	return !state.inFlightSince.IsZero() && current.Sub(state.inFlightSince) < throttleReservationTTL
}

func (throttle *Throttle) prepare() time.Time {
	if throttle.now == nil {
		throttle.now = time.Now
	}
	if throttle.states == nil {
		throttle.states = make(map[string]throttleState)
	}
	current := throttle.now()
	throttle.evictStale(current)
	return current
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
		lastActivity := state.nextAllowedAt
		if state.inFlightSince.After(lastActivity) {
			lastActivity = state.inFlightSince
		}
		if current.Sub(lastActivity) > throttleStaleAfter {
			delete(throttle.states, key)
		}
	}
}

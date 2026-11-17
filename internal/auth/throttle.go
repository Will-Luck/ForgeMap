package auth

import (
	"slices"
	"sync"
	"time"
)

const (
	maxFails  = 5               // wrong passwords in window that refuse a device
	window    = 5 * time.Minute // how long a wrong password counts
	refuseFor = time.Minute     // how long the fifth one refuses
)

// Throttle bounds password guesses. One password is hashed at a time, which
// bounds the CPU. Wrong passwords are counted per device id: every browser
// with no device cookie shares the count of "", and a browser that holds one
// has its own, so guesses from elsewhere never keep the owner out.
type Throttle struct {
	now   func() time.Time
	slot  chan struct{} // the one hashing slot
	mu    sync.Mutex
	fails map[string][]time.Time // failure times per device, younger than window
	until map[string]time.Time   // a device is refused while now is before this
}

// NewThrottle reads now for every decision (nil: time.Now).
func NewThrottle(now func() time.Time) *Throttle {
	if now == nil {
		now = time.Now
	}
	return &Throttle{now: now, slot: make(chan struct{}, 1), fails: map[string][]time.Time{}, until: map[string]time.Time{}}
}

// Begin asks to check one password for this device id ("": none). When
// allowed, the caller hashes and calls finish(ok) once: it frees the slot,
// and counts a failure when ok is false. When not allowed, finish is nil and
// nothing is hashed or counted: the device is refused for now, or another
// password is being hashed.
func (t *Throttle) Begin(device string) (finish func(ok bool), allowed bool) {
	t.mu.Lock()
	refused := t.now().Before(t.until[device])
	t.mu.Unlock()
	if refused {
		return nil, false
	}
	select {
	case t.slot <- struct{}{}:
	default:
		return nil, false
	}
	// A fifth wrong password counted since the check above (its request has
	// just freed the slot) refuses this attempt too.
	t.mu.Lock()
	refused = t.now().Before(t.until[device])
	t.mu.Unlock()
	if refused {
		<-t.slot
		return nil, false
	}
	var once sync.Once
	return func(ok bool) {
		once.Do(func() {
			if !ok {
				t.failed(device)
			}
			<-t.slot
		})
	}, true
}

// Refused reports whether device is refused for now after too many wrong
// passwords. Begin also refuses while another password is being hashed;
// this tells the two apart, for the words of the refusal.
func (t *Throttle) Refused(device string) bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.now().Before(t.until[device])
}

// failed counts one wrong password for device; the fifth within the window
// refuses it for a minute. Only "", ids from a validly signed device cookie
// and the id of a good one-time sign-in token reach here, so the maps stay
// as small as the owner's browsers.
func (t *Throttle) failed(device string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	young := slices.DeleteFunc(t.fails[device], func(at time.Time) bool { return now.Sub(at) >= window })
	t.fails[device] = append(young, now)
	if len(t.fails[device]) >= maxFails {
		t.until[device] = now.Add(refuseFor)
	}
}

package auth

import (
	"fmt"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type clock struct{ at time.Time }

func (c *clock) now() time.Time      { return c.at }
func (c *clock) add(d time.Duration) { c.at = c.at.Add(d) }

// fail counts n wrong passwords for device, one a second.
func fail(t *testing.T, th *Throttle, c *clock, device string, n int) {
	t.Helper()
	for i := range n {
		finish, ok := th.Begin(device)
		if !ok {
			t.Fatalf("wrong password %d for %q refused before it was hashed", i+1, device)
		}
		finish(false)
		c.add(time.Second)
	}
}

func allowed(th *Throttle, device string) bool {
	finish, ok := th.Begin(device)
	if ok {
		finish(true)
	}
	return ok
}

// Five wrong guesses in five minutes refuse unknown browsers for a minute after
// the fifth, never a browser holding a device cookie: each device has its own
// count. Older failures, and successes, count for nothing.
func TestThrottleKeepsDevicesApart(t *testing.T) {
	c := &clock{at: t0}
	th := NewThrottle(c.now)
	fail(t, th, c, "", 5) // the fifth at t0+4s; now t0+5s
	if f, ok := th.Begin(""); ok || f != nil || !allowed(th, "dev-mark") {
		t.Fatal("a sixth unknown guess was allowed, or the owner's browser was kept out by it")
	}
	if c.add(58 * time.Second); allowed(th, "") { // t0+63s
		t.Error("allowed again before the minute was up")
	}
	if c.add(time.Second); !allowed(th, "") { // t0+64s
		t.Error("still refused a minute after the fifth guess")
	}
	c = &clock{at: t0}
	th = NewThrottle(c.now)
	if fail(t, th, c, "dev-mark", 5); allowed(th, "dev-mark") || !allowed(th, "dev-other") || !allowed(th, "") {
		t.Error("a device's five guesses did not refuse it alone")
	}
	c = &clock{at: t0}
	th = NewThrottle(c.now)
	fail(t, th, c, "", 4)
	c.add(6 * time.Minute)
	if fail(t, th, c, "", 1); !allowed(th, "") || !allowed(th, "") {
		t.Error("failures older than five minutes, or successes, still count")
	}
}

// A refusal that starts while an attempt is between its check and the
// hashing slot (another request's fifth wrong password, its slot freed just
// then) still refuses the attempt: Begin reads the refusal again once it has
// the slot, and then frees it. No test can time that race, so a clock that
// answers the second reading inside the refused minute stands in for it.
func TestThrottleChecksAgainWithTheSlot(t *testing.T) {
	c := &clock{at: t0}
	th := NewThrottle(c.now)
	fail(t, th, c, "", 5) // refused until t0+64s
	reads := []time.Time{t0.Add(2 * time.Minute), t0.Add(30 * time.Second)}
	th.now = func() time.Time {
		if len(reads) == 0 {
			return c.at
		}
		at := reads[0]
		reads = reads[1:]
		return at
	}
	if f, ok := th.Begin(""); ok || f != nil || len(reads) != 0 {
		t.Fatalf("allowed %v with a refusal in place when it had the slot (%d clock readings left)", ok, len(reads))
	}
	if c.add(2 * time.Minute); !allowed(th, "") || !allowed(th, "dev-mark") {
		t.Error("the slot was not freed after the second check refused")
	}
}

// Refused tells a device kept out by its wrong passwords from one turned
// away only because another password is being hashed: the caller words the
// two differently.
func TestThrottleRefusedIsTheCountOnly(t *testing.T) {
	c := &clock{at: t0}
	th := NewThrottle(c.now)
	finish, ok := th.Begin("dev-mark")
	if !ok {
		t.Fatal("the first attempt was refused")
	}
	if _, ok := th.Begin(""); ok || th.Refused("") {
		t.Error("a busy hashing slot reads as too many wrong passwords")
	}
	finish(true)
	fail(t, th, c, "", 5) // refused until t0+64s; now t0+5s
	if !th.Refused("") || th.Refused("dev-mark") {
		t.Error("five wrong passwords did not refuse the device alone")
	}
	if c.add(time.Minute); th.Refused("") { // t0+65s
		t.Error("still refused a minute after the fifth wrong password")
	}
}

// One password is hashed at a time: an attempt that cannot have the slot at
// once is refused unhashed (finish nil) and not counted; a second finish
// changes nothing.
func TestThrottleOneHashAtATime(t *testing.T) {
	c := &clock{at: t0}
	th := NewThrottle(c.now)
	finish, ok := th.Begin("")
	if !ok {
		t.Fatal("the first attempt was refused")
	}
	var wg sync.WaitGroup
	var won atomic.Bool
	for i := range 50 {
		wg.Add(1)
		go func() {
			defer wg.Done()
			f, ok := th.Begin(fmt.Sprint("dev-", i%3))
			won.CompareAndSwap(false, ok || f != nil)
		}()
	}
	if wg.Wait(); won.Load() {
		t.Fatal("an attempt was allowed while a hash was running")
	}
	finish(false)
	finish(false)
	if fail(t, th, c, "", 4); allowed(th, "") || !allowed(th, "dev-0") { // five failures in all, none for dev-0
		t.Error("five failures did not refuse: the refused or doubled calls changed the count")
	}
}

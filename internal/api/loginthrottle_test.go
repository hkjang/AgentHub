package api

import (
	"testing"
	"time"
)

func atClock(start time.Time) (*loginThrottle, *time.Time) {
	now := start
	throttle := newLoginThrottle()
	throttle.now = func() time.Time { return now }
	return throttle, &now
}

func TestGuessingAtOneAccountFromOneSourceStops(t *testing.T) {
	throttle, _ := atClock(time.Unix(1_800_000_000, 0))
	for i := 0; i < loginFailsPerAccount-1; i++ {
		if wait := throttle.fail("kim", "10.0.0.1"); wait != 0 {
			t.Fatalf("blocked after %d wrong passwords; a person mistyping their own has to have room", i+1)
		}
	}
	if throttle.fail("kim", "10.0.0.1") <= 0 {
		t.Fatalf("still open after %d wrong passwords from one source at one account", loginFailsPerAccount)
	}
	if throttle.blocked("kim", "10.0.0.1") <= 0 {
		t.Error("the next attempt is not refused, so the block costs the attacker nothing")
	}
}

// The block has to end on its own. A wrong password should slow somebody down,
// not take their account away.
func TestABlockExpires(t *testing.T) {
	throttle, now := atClock(time.Unix(1_800_000_000, 0))
	for i := 0; i < loginFailsPerAccount; i++ {
		throttle.fail("kim", "10.0.0.1")
	}
	if throttle.blocked("kim", "10.0.0.1") <= 0 {
		t.Fatal("the block was never applied, so this proves nothing")
	}
	*now = now.Add(loginBlockBase + time.Second)
	if wait := throttle.blocked("kim", "10.0.0.1"); wait > 0 {
		t.Errorf("still blocked %v after the block should have expired", wait)
	}
}

// Nobody may lock somebody else out by failing on their name. This is why there
// is no username-only tight limit: a hostile source hitting one account must not
// stop that account's own person from logging in.
func TestOneSourceCannotShutAnotherOut(t *testing.T) {
	throttle, _ := atClock(time.Unix(1_800_000_000, 0))
	for i := 0; i < loginFailsPerAccount*3; i++ {
		throttle.fail("kim", "203.0.113.9")
	}
	if wait := throttle.blocked("kim", "10.0.0.1"); wait > 0 {
		t.Errorf("kim is locked out of their own address for %v because somebody else guessed at their name", wait)
	}
}

// Spraying many usernames from one source is the other shape an attack takes,
// and the per-account limit never fires on it because no account repeats.
func TestSprayingManyNamesFromOneSourceStops(t *testing.T) {
	throttle, _ := atClock(time.Unix(1_800_000_000, 0))
	blockedAt := 0
	for i := 1; i <= loginFailsPerSource+5; i++ {
		if throttle.fail(string(rune('a'+i%26))+"user", "198.51.100.7") > 0 && blockedAt == 0 {
			blockedAt = i
		}
	}
	if blockedAt == 0 {
		t.Fatalf("%d guesses at %d different names from one source and nothing stopped", loginFailsPerSource+5, loginFailsPerSource+5)
	}
	if blockedAt > loginFailsPerSource {
		t.Errorf("the source limit fired at %d, past its own allowance of %d", blockedAt, loginFailsPerSource)
	}
}

// A successful login clears what this source did wrong, so somebody who mistyped
// four times and then got it right is not carrying a near-block around.
func TestSucceedingClearsTheSourcesFailures(t *testing.T) {
	throttle, _ := atClock(time.Unix(1_800_000_000, 0))
	for i := 0; i < loginFailsPerAccount-1; i++ {
		throttle.fail("kim", "10.0.0.1")
	}
	throttle.succeed("kim", "10.0.0.1")
	for i := 0; i < loginFailsPerAccount-1; i++ {
		if wait := throttle.fail("kim", "10.0.0.1"); wait != 0 {
			t.Fatalf("blocked after %d wrong passwords following a success; the count was not cleared", i+1)
		}
	}
}

// The map is reachable by anyone who can reach the login route, so it has to
// have a lid: an attacker inventing usernames would otherwise turn the throttle
// into the memory exhaustion it exists to prevent.
func TestTheThrottleDoesNotGrowWithoutLimit(t *testing.T) {
	throttle, _ := atClock(time.Unix(1_800_000_000, 0))
	for i := 0; i < loginThrottleKeys+500; i++ {
		throttle.fail(string(rune(i))+"invented", "198.51.100.7")
	}
	if len(throttle.windows) > loginThrottleKeys {
		t.Errorf("the throttle holds %d windows, past its own cap of %d", len(throttle.windows), loginThrottleKeys)
	}
}

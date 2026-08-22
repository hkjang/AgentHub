package api

import (
	"sync"
	"time"
)

// Guessing passwords at the door.
//
// The login route had nothing in front of it. Sixty wrong passwords went through
// this deployment in 2.4 seconds and the account opened on the sixty-first
// attempt with the right one — measured, not reasoned about. One client managed
// twenty-five guesses a second; several clients multiply that, and every guess
// also costs a bcrypt, so the same route is an unauthenticated way to spend the
// control plane's CPU.
//
// What it does not do is lock an account. A username-only lock hands anybody a
// way to keep somebody else out by failing on their name, which trades a slow
// attack for a reliable denial of service. So the tight limits are per source:
// the same source guessing at one account, and one source guessing at all of
// them. The per-account limit is deliberately loose and short — it bounds a
// distributed attack on one account without giving anyone a lockout to abuse.
//
// It is per-process, which is the honest description: with more than one control
// plane replica an attacker gets one allowance per replica. That is a large
// reduction from unlimited and not the same thing as a shared counter, and the
// deployment this ships with runs one.
type loginThrottle struct {
	mu      sync.Mutex
	windows map[string]*attemptWindow
	now     func() time.Time
	// dropped counts live blocks thrown away to stay under the cap. It is read by
	// the guard that proves the cap holds, and it is not expected to move.
	dropped int
}

type attemptWindow struct {
	failures  int
	firstFail time.Time
	blockedTo time.Time
	blocks    int
}

// The limits.
//
// Per (source, account) is the one that stops a targeted attack, and it is tight:
// a person mistyping their own password has room to, and past that a source is
// guessing at somebody.
//
// The other two are backstops against spraying, and they are deliberately loose
// because of who else lands on them. An ingress that does not pass the client
// address makes every person in a company share one apparent source, so a limit
// small enough to catch a bad Monday morning would shut the deployment's door on
// everybody — a defence that takes the platform down with the attacker is not
// one. Sixty failures in fifteen minutes from one address is far past a bad
// morning and still a hard bound on how much bcrypt an address can spend.
const (
	loginWindow          = 15 * time.Minute
	loginFailsPerAccount = 5
	loginFailsPerSource  = 60
	loginFailsPerName    = 100
	loginBlockBase       = time.Minute
	loginBlockMax        = 15 * time.Minute
	// loginThrottleKeys bounds the map. An attacker spraying invented usernames
	// would otherwise be handed a way to grow it without limit — the throttle
	// would become the memory leak it was added to prevent. Expired windows are
	// dropped on the way past; if the map is still at the cap, the oldest goes.
	loginThrottleKeys = 20000
)

func newLoginThrottle() *loginThrottle {
	return &loginThrottle{windows: map[string]*attemptWindow{}, now: time.Now}
}

// blocked reports how long this attempt must wait, and zero if it may proceed.
// It is asked before the password is checked, so a blocked attempt costs no
// bcrypt.
func (t *loginThrottle) blocked(username, ip string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	longest := time.Duration(0)
	for _, key := range t.keys(username, ip) {
		window := t.windows[key.name]
		if window == nil {
			continue
		}
		if wait := window.blockedTo.Sub(now); wait > longest {
			longest = wait
		}
	}
	return longest
}

// fail records one wrong password and returns how long the next attempt waits.
func (t *loginThrottle) fail(username, ip string) time.Duration {
	t.mu.Lock()
	defer t.mu.Unlock()
	now := t.now()
	t.sweep(now)
	longest := time.Duration(0)
	for _, key := range t.keys(username, ip) {
		window := t.windows[key.name]
		if window == nil || now.Sub(window.firstFail) > loginWindow {
			window = &attemptWindow{firstFail: now}
			t.windows[key.name] = window
		}
		window.failures++
		if window.failures >= key.allowed {
			window.blocks++
			window.failures = 0
			window.firstFail = now
			window.blockedTo = now.Add(min(loginBlockBase<<(window.blocks-1), loginBlockMax))
		}
		if wait := window.blockedTo.Sub(now); wait > longest {
			longest = wait
		}
	}
	return longest
}

// succeed forgets this source's failures. The account's own window is left
// alone: it is not this source's to clear, and clearing it would hand a
// distributed attack a free reset every time one of its guesses landed
// somewhere else.
func (t *loginThrottle) succeed(username, ip string) {
	t.mu.Lock()
	defer t.mu.Unlock()
	delete(t.windows, "account\x00"+username+"\x00"+ip)
	delete(t.windows, "source\x00"+ip)
}

type throttleKey struct {
	name    string
	allowed int
}

func (t *loginThrottle) keys(username, ip string) []throttleKey {
	return []throttleKey{
		{"account\x00" + username + "\x00" + ip, loginFailsPerAccount},
		{"source\x00" + ip, loginFailsPerSource},
		{"name\x00" + username, loginFailsPerName},
	}
}

// sweep keeps the map under its cap, in three passes of decreasing politeness.
//
// Expired windows go first and cost nothing. If the map is still full, windows
// that are not holding anybody back go next: a forgotten failure count is a
// smaller loss than a map that grows until the process dies. If it is somehow
// still full — every window blocking, which takes an attacker five failures each
// to arrange — live blocks are dropped too, and that is said out loud in the
// only way this type can say it. The alternative is to let the defence become
// the exhaustion it was added to prevent.
func (t *loginThrottle) sweep(now time.Time) {
	if len(t.windows) < loginThrottleKeys {
		return
	}
	for key, window := range t.windows {
		if now.After(window.blockedTo) && now.Sub(window.firstFail) > loginWindow {
			delete(t.windows, key)
		}
	}
	room := loginThrottleKeys * 9 / 10
	for key, window := range t.windows {
		if len(t.windows) < room {
			break
		}
		if !now.Before(window.blockedTo) {
			delete(t.windows, key)
		}
	}
	for key := range t.windows {
		if len(t.windows) < room {
			break
		}
		t.dropped++
		delete(t.windows, key)
	}
}

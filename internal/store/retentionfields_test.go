package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

// A retention knob has to exist in three places: the console offers it, Validate
// gives it a floor, and Cleanup actually sweeps something with it. A field missing
// from any one of them is a number somebody types that does nothing — the console
// saves it, the screen shows it, and the table it names grows forever.
//
// Notifications were the other half of this: not a knob missing a sweep, but a
// table with neither. Ninety-nine rows in a deployment with one user, and nothing
// anywhere that would ever remove one.
func TestEveryRetentionKnobIsOfferedFlooredAndSwept(t *testing.T) {
	cleanup, err := os.ReadFile("operations.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(cleanup)
	console, consoleErr := os.ReadFile(filepath.Join("..", "..", "web", "src", "pages", "AdminExecution.tsx"))

	policy := reflect.TypeOf(RetentionPolicy{})
	sweeps := source[strings.Index(source, "sweeps := []struct"):strings.Index(source, "for _, sweep := range sweeps")]
	floors := source[strings.Index(source, "func (policy RetentionPolicy) Validate()"):]
	if end := strings.Index(floors, "\n}\n"); end >= 0 {
		floors = floors[:end]
	}
	for i := 0; i < policy.NumField(); i++ {
		field := policy.Field(i)
		tag := strings.Split(field.Tag.Get("json"), ",")[0]
		if !strings.Contains(sweeps, "policy."+field.Name) {
			t.Errorf("%s is a retention setting that sweeps nothing; the table it names grows forever while the console shows a number for it", field.Name)
		}
		if !strings.Contains(floors, "policy."+field.Name) {
			t.Errorf("%s has no minimum; somebody can set it to one day and lose history they are still reading", field.Name)
		}
		if consoleErr == nil && !strings.Contains(string(console), "'"+tag+"'") {
			t.Errorf("the console does not offer %s; a retention setting nobody can reach is one nobody will use", tag)
		}
	}
	// And the policy has to serialise as the console reads it.
	encoded, err := json.Marshal(RetentionPolicy{NotificationDays: 30})
	if err != nil || !strings.Contains(string(encoded), `"notificationDays":30`) {
		t.Errorf("the policy does not round-trip through the name the console uses: %s", encoded)
	}
}

// An unread notice is still work, which is why the sweep leaves it alone — the
// same reason the event sweep skips undelivered events.
func TestTheNotificationSweepLeavesUnreadNoticesAlone(t *testing.T) {
	body, err := os.ReadFile("operations.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, `{name: "notifications"`)
	if at < 0 {
		t.Fatal("the notification sweep is gone; nothing removes read notices any more")
	}
	sweep := source[at : strings.Index(source[at:], "},")+at]
	if !strings.Contains(sweep, "read_at IS NOT NULL") {
		t.Error("the notification sweep does not check that a notice was read; it will delete things nobody has seen")
	}
}

// Sessions are swept whether or not anybody configured retention, because an
// expired session is not a record — it is a row that cannot authenticate a request
// and sits in the index that every authenticated request reads.
func TestExpiredSessionsAreSweptWithoutBeingAskedTo(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "execution", "caretaker.go"))
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (c *Caretaker) expireSessions(")
	if at < 0 {
		t.Fatal("nothing removes expired sessions; the table grows by one row per login forever")
	}
	fn := source[at:]
	if end := strings.Index(fn, "\n}\n"); end >= 0 {
		fn = fn[:end]
	}
	if strings.Contains(fn, "Retention") {
		t.Error("the session sweep reads the retention policy; it must not wait for somebody to configure history retention")
	}
	loop := source[strings.Index(source, "func (c *Caretaker) Run("):]
	if !strings.Contains(loop[:strings.Index(loop, "\n}\n")], "expireSessions(ctx)") {
		t.Error("expireSessions is never called; it exists and does nothing")
	}
}

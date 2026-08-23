package store

import (
	"os"
	"strings"
	"testing"
)

// What a trigger's record has to say.
//
// A trigger stored when it last fired and nothing about what came of it, so a
// schedule firing every hour into a task that fails every hour read exactly like
// one that works. These are the properties that make the record worth showing.
func TestATriggersRecordCountsWhatItProduced(t *testing.T) {
	body, err := os.ReadFile("triggerhealth.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) TriggerHealthFor(")
	if at < 0 {
		t.Fatal("TriggerHealthFor is gone; this guard is reading nothing")
	}
	query := source[at:]
	if end := strings.Index(query, "\nfunc "); end >= 0 {
		query = query[:end]
	}

	// Only tasks a trigger created. Counting an agent's manual runs would report
	// automation as healthy because somebody ran it by hand.
	if !strings.Contains(query, "t.trigger_id IS NOT NULL") {
		t.Error("the record counts tasks that no trigger created")
	}
	// Failures separately from the total: "twelve tasks" is not an answer to
	// "is this working".
	if !strings.Contains(query, "FILTER (WHERE t.status IN ('failed', 'dead_letter'))") {
		t.Error("the record does not count how many of those tasks failed")
	}
	// The newest first, so "how did it end" is the last ending rather than an
	// arbitrary one.
	if !strings.Contains(query, "ORDER BY t.created_at DESC") {
		t.Error("the last status is taken from an arbitrary task rather than the most recent")
	}
	// One query for every trigger. A number nobody can afford to show is not a
	// number.
	if strings.Count(query, "s.pool.Query") != 1 {
		t.Error("the record is read per trigger; a console listing twenty would make twenty round trips")
	}
	// Scoped to the caller. Reading another owner's automation is not this
	// listing's business.
	if !strings.Contains(query, "t.owner_id = $2") {
		t.Error("the record is not scoped to the owner asking for it")
	}
}

// TestAnOverdueScheduleIsCountedAgainstEnabledOnes — a disabled trigger is not
// late, and a webhook has no next firing at all. Counting either would report a
// deployment as broken for choices somebody made deliberately.
func TestAnOverdueScheduleIsCountedAgainstEnabledOnes(t *testing.T) {
	body, err := os.ReadFile("triggerhealth.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) OverdueTriggers(")
	if at < 0 {
		t.Fatal("OverdueTriggers is gone; this guard is reading nothing")
	}
	query := source[at:]
	if !strings.Contains(query, "WHERE enabled AND type = ") {
		t.Error("overdue counts disabled triggers or ones that have no schedule to be late for")
	}
	if !strings.Contains(query, "next_fire_at IS NOT NULL") {
		t.Error("a trigger that has never been scheduled is counted as overdue")
	}
}

// TestTheOverdueQueryAsksForATypeThatExists is here because the first version did
// not.
//
// It filtered on type = 'schedule'; the schema allows manual, cron, webhook and
// event. The query therefore matched nothing, reported nothing, and looked
// exactly like a deployment whose schedules were all on time. A value the code
// believes in and the database has never held is the quietest kind of wrong.
func TestTheOverdueQueryAsksForATypeThatExists(t *testing.T) {
	body, err := os.ReadFile("triggerhealth.go")
	if err != nil {
		t.Fatal(err)
	}
	query := string(body)
	at := strings.Index(query, "func (s *Store) OverdueTriggers(")
	if at < 0 {
		t.Fatal("OverdueTriggers is gone; this guard is reading nothing")
	}
	query = query[at:]

	// The types the database will accept, read from the migration that declares
	// them rather than from a list kept here.
	migrations, err := os.ReadDir("migrations")
	if err != nil {
		t.Fatal(err)
	}
	allowed := ""
	for _, entry := range migrations {
		text, err := os.ReadFile("migrations/" + entry.Name())
		if err != nil {
			t.Fatal(err)
		}
		if at := strings.Index(string(text), "agent_triggers_type_check CHECK (type IN ("); at >= 0 {
			rest := string(text)[at:]
			allowed = rest[strings.Index(rest, "(")+1 : strings.Index(rest, ")")]
		}
	}
	if allowed == "" {
		t.Fatal("the trigger types are not declared in any migration; this guard is reading nothing")
	}

	// Whatever type the query names has to be one of them.
	at = strings.Index(query, "type = '")
	if at < 0 {
		t.Fatal("the overdue query no longer filters by type; it would count webhooks that have no schedule to be late for")
	}
	rest := query[at+len("type = '"):]
	asked := rest[:strings.Index(rest, "'")]
	if !strings.Contains(allowed, "'"+asked+"'") {
		t.Errorf("the overdue query asks for type %q, which the database never holds (허용: %s) — it would report nothing forever and read as healthy", asked, allowed)
	}
}

// TestEveryKindOfTriggerRecordsThatItFired — only the scheduler did, through the
// statement that also advances the next firing. So a webhook that had accepted a
// thousand deliveries read "never fired", and so did every event trigger; the
// console line that says exactly that was therefore wrong for two of the three
// kinds.
func TestEveryKindOfTriggerRecordsThatItFired(t *testing.T) {
	for _, source := range []struct{ kind, file string }{
		{"웹훅", "../api/execution.go"},
		{"이벤트", "../execution/dispatcher.go"},
	} {
		body, err := os.ReadFile(source.file)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "RecordTriggerFired(") {
			t.Errorf("%s 트리거는 작업을 만들고도 발화한 사실을 남기지 않습니다 (%s) — 화면에는 '아직 한 번도 실행되지 않았습니다' 로 남습니다",
				source.kind, source.file)
		}
	}
}

// An event trigger that matches nothing and an event that never happens are
// opposite problems, and they look identical: no tasks, no errors, nothing.
func TestAnEventTriggersReachSeparatesTwoSilences(t *testing.T) {
	body, err := os.ReadFile("triggerhealth.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) EventReachFor(")
	if at < 0 {
		t.Fatal("EventReachFor is gone; this guard is reading nothing")
	}
	query := source[at:]
	if end := strings.Index(query, "\nfunc "); end >= 0 {
		query = query[:end]
	}
	// How many events of the type happened at all, whatever their payload. That
	// is the half that says "the filter is wrong" rather than "nothing happened".
	if !strings.Contains(query, "e.type = t.event_type") {
		t.Error("reach does not count the events this trigger's type produced")
	}
	// And how many actually reached it. Deliveries rather than tasks: a trigger
	// can match and still fail to create a task, and this question is about the
	// filter.
	if !strings.Contains(query, "event_deliveries") {
		t.Error("reach counts tasks rather than deliveries, so a matched event that failed afterwards reads as a filter that missed")
	}
	if !strings.Contains(query, "t.type = 'event'") {
		t.Error("reach is computed for triggers that have no filter to be wrong")
	}
}

// TestAFilterFieldNobodyPublishesIsSaidAndNotRefused — a filter is matched by
// containment, so a misspelled field matches nothing, silently, for ever. The
// evidence is what this deployment has published, which is evidence and not
// proof: a correct filter on a new deployment must still save.
func TestAFilterFieldNobodyPublishesIsSaidAndNotRefused(t *testing.T) {
	body, err := os.ReadFile("../api/execution.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func unseenFilterKeys(")
	if at < 0 {
		t.Fatal("the filter check is gone; this guard is reading nothing")
	}
	check := source[at:]
	if end := strings.Index(check, "\nfunc "); end >= 0 {
		check = check[:end]
	}
	// No evidence means no complaint.
	if !strings.Contains(check, "len(seen) == 0") {
		t.Error("a deployment that has published nothing still complains about the filter")
	}
	// And the complaint never becomes a refusal: the save path must not return an
	// error for it.
	saveAt := strings.Index(source, "func (s *Server) saveAgentTrigger(")
	save := source[saveAt:]
	if end := strings.Index(save, "\nfunc "); end >= 0 {
		save = save[:end]
	}
	if strings.Contains(save, "writeError(w, http.StatusBadRequest, \"unseen_filter") {
		t.Error("a filter this deployment has not seen is refused rather than reported")
	}
	if !strings.Contains(save, "notice") {
		t.Error("the filter warning is computed and then not told to anybody")
	}
}

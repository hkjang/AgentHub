package api

import (
	"os"
	"strings"
	"testing"
)

// A list of dependencies cannot report the one that is not there.
//
// The readiness screen asked each configured model endpoint and each configured
// MCP server how it was, which answers everything except "there aren't any" — and
// that is the state a new deployment is in. With no model endpoint every prose,
// flow and investigation agent fails the moment it calls a model, and this screen
// reported no problems.
//
// The worker is the same shape and worse. A control plane with no worker looks
// healthy from every angle: the console answers, agents save, tasks queue, and
// nothing ever claims one. It is the most common way a first deployment stalls.
func TestReadinessReportsWhatIsMissingAndNotOnlyWhatIsBroken(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) readiness(")
	if at < 0 {
		t.Fatal("the readiness handler is gone; this guard is reading nothing")
	}
	handler := source[at:]
	if end := strings.Index(handler, "\nfunc "); end >= 0 {
		handler = handler[:end]
	}
	// The row and its verdict together. Checking for the verdict alone passed on
	// the cluster's own "unconfigured" row, which is a guard that cannot fail —
	// exactly what it is here to catch in the code it guards.
	for _, absence := range []struct{ what, evidence string }{
		{"no model endpoint is configured", `Area: "모델", Name: "모델 엔드포인트", Verdict: "unconfigured"`},
		{"no worker is running", `Area: "실행", Name: "워커", Verdict: "none"`},
		{"execution is paused", `Area: "실행", Name: "워커", Verdict: "paused"`},
		{"the cluster is not configured at all", `Area: "Kubernetes", Name: "클러스터", Verdict: "unconfigured"`},
	} {
		if !strings.Contains(handler, absence.evidence) {
			t.Errorf("readiness does not report that %s; the screen says nothing is wrong while nothing can run", absence.what)
		}
	}
	if !strings.Contains(handler, "LiveWorkers(") {
		t.Error("readiness never asks whether a worker is alive")
	}
	// The verdicts it produces have to be ones the summary counts as problems,
	// otherwise the row appears and the count beside it still reads zero.
	for _, verdict := range []string{"unconfigured", "none", "unknown"} {
		if readinessOK[verdict] {
			t.Errorf("%q is treated as a passing verdict; a deployment that cannot run anything would report no problems", verdict)
		}
	}
	// Paused is a decision rather than a fault, but it still has to be visible: it
	// is the answer to "why is nothing running".
	if readinessOK["paused"] {
		t.Error("a paused execution plane is counted as fine; it is the answer to why nothing is running")
	}
}

// And it cannot report something that is not there either.
//
// The stuck-runtime row is read from the platform's own record, and the record
// is only corrected when somebody opens a page that observes runtimes. Deleting
// a runtime that will not start is the ordinary answer to one, so the record
// keeps saying "starting, wanted running" about an object the cluster no longer
// has — and this screen, whose whole job is "what is broken now", reported it.
//
// Measured: an AgentRuntime deleted by hand was still reported here twelve
// minutes later, quoting the image error it had died of, with a link that cured
// the report by being visited.
func TestReadinessDoesNotReportRuntimesTheClusterNoLongerHas(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "RuntimesStuckStarting(")
	if at < 0 {
		t.Fatal("the stuck-runtime check is gone; this guard is reading nothing")
	}
	check := source[at:]
	if end := strings.Index(check, "\n\t})"); end >= 0 {
		check = check[:end]
	}
	if !strings.Contains(check, "StatusAll(") {
		t.Error("the stuck-runtime check reports the record without asking the cluster whether the runtime still exists")
	}
	forget := strings.Index(check, "ForgetMissingRuntime(")
	report := strings.Index(check, `Verdict: "stuck"`)
	if forget < 0 {
		t.Error("a runtime the cluster no longer has is left asking to be run, so it is reported again ten minutes later")
	}
	if report < 0 {
		t.Fatal("nothing is reported as stuck any more; this guard is reading nothing")
	}
	if forget > report {
		t.Error("the phantom is reported before it is written off")
	}
	if !strings.Contains(check, "continue") {
		t.Error("a runtime that is gone from the cluster is still added to the screen")
	}
}

// And it does not claim the half it is not doing.
//
// Inspecting requests and leaving model answers alone is a real choice — it is
// the expensive half, and the settings keep it separate for that reason. But
// the row said "8가지 데이터 종류를 검사합니다" either way, so a deployment whose
// answers were never looked at read as one where they were. The answer is the
// direction that carries data into this platform's own store and out through
// whatever reads a run afterwards.
func TestReadinessSaysWhichDirectionIsInspected(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "scannedClasses(settings)")
	if at < 0 {
		t.Fatal("the content-scanner check is gone; this guard is reading nothing")
	}
	check := source[at:]
	if end := strings.Index(check, "\n\t})"); end >= 0 {
		check = check[:end]
	}
	if !strings.Contains(check, "settings.ScanResponses") {
		t.Error("the row reads the same whether or not model answers are inspected")
	}
	if !strings.Contains(check, "답변은 검사하지 않습니다") {
		t.Error("a deployment that never inspects an answer is not told so")
	}
	if !strings.Contains(check, "요청과 응답에서 검사합니다") {
		t.Error("a deployment that inspects both is not told which")
	}
}

// A failed step can still hold the account of what happened.
//
// The console showed step.error *instead of* step.output, so a step that
// carried both showed only the failure. Measured on a cluster after the fabric
// backend began recording each worker's own last words: steps with 233 bytes of
// output and a 14-byte error, of which a person was shown the 14 bytes.
func TestTheConsoleShowsAFailedStepsOutputToo(t *testing.T) {
	body, err := os.ReadFile("../../web/src/pages/Tasks.tsx")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if strings.Contains(source, "{step.error ? <p className=\"run-error\">{step.error}</p> : <pre") {
		t.Error("a step's output is shown only when it did not fail, so what a failed run recorded is hidden")
	}
	if !strings.Contains(source, "{step.error && <p className=\"run-error\">{step.error}</p>}") {
		t.Error("the step's failure is no longer shown")
	}
	if !strings.Contains(source, "{step.output && <pre className=\"custom-scroll\">{step.output}</pre>}") {
		t.Error("the step's output is no longer shown")
	}
}

// Taking a runtime away from work is a decision, not a click.
//
// The platform has always known why a runtime must not be stopped — the idle
// sweeper, the warm pool and the release path all ask RuntimeBusy — and the one
// place a person presses the button never did.
//
// Measured on a cluster: a task was running, the console's stop returned 202
// with no warning, and the task died as "ACP 실행이 실패했습니다: 에이전트가
// 응답을 끝내지 않고 종료했습니다" — blaming the agent for a decision a person
// had just made.
func TestStoppingABusyRuntimeAsksFirst(t *testing.T) {
	body, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	action := source[strings.Index(source, "func (s *Server) runtimeAction("):]
	if end := strings.Index(action, "\n// forceRequested"); end >= 0 {
		action = action[:end]
	}
	if !strings.Contains(action, "s.store.RuntimeBusy(") {
		t.Error("a person can stop a runtime out from under running work without being told")
	}
	if !strings.Contains(action, `"runtime_busy"`) {
		t.Error("the refusal has no code, so a screen cannot offer to confirm it")
	}
	if !strings.Contains(action, "forceRequested(r)") {
		t.Error("there is no way to say it was meant — stopping a stuck runtime is what the button is for")
	}
	restart := source[strings.Index(source, "func (s *Server) restartRuntime("):]
	if end := strings.Index(restart, "\nfunc "); end >= 0 {
		restart = restart[:end]
	}
	if !strings.Contains(restart, "s.store.RuntimeBusy(") {
		t.Error("a restart takes the Pod away exactly as a stop does, and asks nobody")
	}
}

// And the answer has to survive the trip to the screen.
func TestTheConsoleCanTellOneRefusalFromAnother(t *testing.T) {
	body, err := os.ReadFile("../../web/src/api.ts")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, "export class ApiError") {
		t.Error("the API's error code is dropped in the client, so no screen can act on which refusal it was")
	}
	if strings.Contains(source, "throw new Error(payload.error") {
		t.Error("a refusal is still thrown as a bare message somewhere")
	}
	page, err := os.ReadFile("../../web/src/pages/Agents.tsx")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(page), "err.code==='runtime_busy'") {
		t.Error("the console shows the refusal as an error banner with no way to answer it")
	}
}

// And when somebody confirms, the platform writes down what they ended.
//
// A forced stop killed work that was running. In the audit log it read exactly
// like stopping an idle runtime — while the task it ended said only that the
// agent had quit, so between the two records there was nothing connecting a
// dead task to the person who ended it.
func TestForcingAStopRecordsWhatItEnded(t *testing.T) {
	body, err := os.ReadFile("routes.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	for _, part := range []struct{ name, from, to string }{
		{"stop", "func (s *Server) runtimeAction(", "\n// forceRequested"},
		{"restart", "func (s *Server) restartRuntime(", "\nfunc "},
	} {
		at := strings.Index(source, part.from)
		if at < 0 {
			t.Fatalf("%s is gone; this guard is reading nothing", part.name)
		}
		handler := source[at:]
		if end := strings.Index(handler, part.to); end >= 0 {
			handler = handler[:end]
		}
		if !strings.Contains(handler, `"overrode"`) {
			t.Errorf("a forced %s is audited exactly like one that ended nothing", part.name)
		}
		audit := strings.Index(handler, "s.store.Audit(")
		if audit < 0 {
			t.Errorf("%s is not audited at all", part.name)
			continue
		}
		if strings.Contains(handler[audit:], "clientIP(r), nil)") {
			t.Errorf("%s still audits with no detail, so the override is dropped", part.name)
		}
	}
}

// Whether a person is at a keyboard has to keep being true.
//
// RuntimeBusy reads it from runtime_sessions.updated_at — "one touched in the
// last few minutes is a person at a keyboard" — and nothing ever touched that
// row after the session opened. Fifteen minutes in, somebody working looked
// exactly like somebody who had walked away: the idle sweeper culls the runtime
// they are using, and the confirmation before stopping one never appears.
func TestAPersonWorkingKeepsLookingLikeOne(t *testing.T) {
	store, err := os.ReadFile("../store/runtime.go")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(store), "func (s *Store) TouchRuntimeSessions(") {
		t.Fatal("nothing can refresh a session; the evidence expires and this guard is reading nothing")
	}
	if !strings.Contains(string(store), "UPDATE runtime_sessions SET updated_at=now() WHERE runtime_id=$1 AND status='active'") {
		t.Error("a closed session is refreshed, or an open one is not")
	}
	// Every place person traffic refreshes the runtime must refresh the session
	// too. A task's traffic must not: it would make an abandoned session look
	// attended, which is the same lie in the other direction.
	for _, file := range []string{"session.go", "runtimepath.go"} {
		body, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		source := string(body)
		runtimeTouches := strings.Count(source, "s.store.TouchRuntime(r.Context()")
		sessionTouches := strings.Count(source, "s.store.TouchRuntimeSessions(r.Context()")
		if runtimeTouches != sessionTouches {
			t.Errorf("%s refreshes the runtime %d times and the session %d — a person's traffic stops counting after the window",
				file, runtimeTouches, sessionTouches)
		}
	}
	worker, err := os.ReadFile("../execution/orchestrator.go")
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(worker), "TouchRuntimeSessions(") {
		t.Error("a task's own traffic marks a session attended, so an abandoned one never expires")
	}
}

// A terminal is one request, and then an hour of somebody working.
//
// The proxy upgrades the connection and hands it over; no further HTTP request
// arrives for that person until they close the tab. Refreshing the session where
// the request lands therefore marks it live exactly once — so the fix that made
// a person keep looking like one worked for every kind of traffic except the
// kind people actually use.
func TestAnOpenConnectionKeepsSayingSomebodyIsThere(t *testing.T) {
	body, err := os.ReadFile("session.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Server) serveRuntimeProxy(")
	if at < 0 {
		t.Fatal("the proxy is gone; this guard is reading nothing")
	}
	proxy := source[at:]
	if end := strings.Index(proxy, "\n// holdRuntimePresence"); end >= 0 {
		proxy = proxy[:end]
	}
	if !strings.Contains(proxy, "holdRuntimePresence(") {
		t.Error("an upgraded connection is marked live once and then goes stale under somebody who never stopped")
	}
	if !strings.Contains(proxy, `strings.EqualFold(r.Header.Get("Upgrade"), "websocket")`) {
		t.Error("every request holds presence, including the ones that end immediately")
	}
	hold := source[strings.Index(source, "func (s *Server) holdRuntimePresence("):]
	if end := strings.Index(hold, "\n// runtimePresenceInterval"); end >= 0 {
		hold = hold[:end]
	}
	// It must stop. A goroutine that outlives the connection would report a
	// closed tab as a person for ever, which is the same lie the other way.
	if !strings.Contains(hold, "case <-done:") || !strings.Contains(hold, "case <-ctx.Done():") {
		t.Error("presence outlives the connection that justified it")
	}
	if !strings.Contains(hold, "TouchRuntimeSessions(") {
		t.Error("the connection refreshes the runtime but not the session that is read for presence")
	}
	if !strings.Contains(source, "runtimePresenceInterval = 2 * time.Minute") {
		t.Error("the interval is not comfortably inside the fifteen minutes that decide presence")
	}
}

// Work that is waiting on a limit is not work that is waiting its turn.
//
// A task the platform will not start yet goes back on the queue without spending
// an attempt — the right thing to do, and invisible to everything that watches
// for trouble: nothing failed, nothing was abandoned, no runtime is stuck. A
// deployment whose queue has stopped moving because a limit is full reads as a
// quiet afternoon.
//
// Measured on a cluster: with the runtime quota full, a task sat queued with
// "사용자 Runtime Quota(1개)를 초과합니다" written on it, and this screen said
// the deployment was fine.
func TestReadinessSaysWhenWorkIsWaitingOnALimit(t *testing.T) {
	body, err := os.ReadFile("readiness.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	if !strings.Contains(source, "s.store.TasksHeldBack(") {
		t.Error("work parked on a limit is never counted, so a stalled queue looks like a quiet one")
	}
	if !strings.Contains(source, `Name: "대기 중인 작업"`) {
		t.Error("the row has no name a person can look for")
	}
	// The reason is the whole point: "queued" says nothing, "quota full" says
	// what to do.
	at := strings.Index(source, "s.store.TasksHeldBack(")
	check := source[at:]
	if end := strings.Index(check, "\n\t})"); end >= 0 {
		check = check[:end]
	}
	if !strings.Contains(check, "held.Reason") {
		t.Error("the row counts the work without saying what it is waiting for")
	}
	store, err := os.ReadFile("../store/execution.go")
	if err != nil {
		t.Fatal(err)
	}
	query := string(store)[strings.Index(string(store), "func (s *Store) TasksHeldBack("):]
	if end := strings.Index(query, "\n// TasksAbandoned"); end >= 0 {
		query = query[:end]
	}
	// A task with no reason on it is waiting its turn, which is not news.
	if !strings.Contains(query, "coalesce(waiting_reason,'') <> ''") {
		t.Error("every queued task is reported, including the ones simply waiting their turn")
	}
	// And a limit that clears in a minute is the system working.
	if !strings.Contains(query, "created_at < now() - $1::interval") {
		t.Error("work is reported the instant it is held, so a queue that is moving reads as blocked")
	}
	// Aged from when the work was asked for. A held task is put back on the queue
	// every few seconds, so a grace period counted from updated_at never elapses
	// — measured: twelve minutes held, updated_at 26 seconds old.
	if strings.Contains(query, "updated_at < now()") {
		t.Error("the grace is counted from a timestamp the retry keeps refreshing, so the row can never appear")
	}
}

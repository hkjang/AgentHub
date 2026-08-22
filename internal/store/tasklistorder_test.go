package store

import (
	"os"
	"strings"
	"testing"
)

// A capped list ordered newest first is right for a log and wrong for a work
// queue. A task waiting for an approval, or handed to a person, is exactly the
// task that gets older — so on a busy deployment it slides past the end of the
// page and stops existing as far as the screen is concerned. The longer somebody
// leaves it, the harder it is to find.
//
// So the list puts unfinished work above finished work, and the ordering has to
// keep saying that.
func TestTheTaskListPutsUnfinishedWorkFirst(t *testing.T) {
	body, err := os.ReadFile("execution.go")
	if err != nil {
		t.Fatal(err)
	}
	source := string(body)
	at := strings.Index(source, "func (s *Store) AgentTasks(")
	if at < 0 {
		t.Fatal("AgentTasks is gone; this guard is reading nothing")
	}
	list := source[at:]
	if end := strings.Index(list, "\n}\n"); end >= 0 {
		list = list[:end]
	}
	if !strings.Contains(list, "taskUnfinishedFirst") {
		t.Error("the task list is ordered by time alone; work waiting on a person ages off the end of the page")
	}
	// And the set it sorts on has to be the terminal one. A status missing from it
	// sorts as unfinished forever, which is the same bug pointing the other way.
	for _, status := range []string{TaskCompleted, TaskFailed, TaskDeadLetter, TaskCancelled} {
		if !strings.Contains(taskFinished, "'"+status+"'") {
			t.Errorf("%s is not counted as finished; it will sit at the top of every task list forever", status)
		}
	}
	for _, status := range []string{TaskQueued, TaskRunning, TaskBlocked, TaskHandoff, "waiting_approval"} {
		if strings.Contains(taskFinished, "'"+status+"'") {
			t.Errorf("%s is counted as finished; work that still needs somebody would sort below work that does not", status)
		}
	}
}

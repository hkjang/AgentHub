package quota

import (
	"errors"
	"fmt"
)

// ErrExceeded marks a refusal by a limit rather than a failure to evaluate one.
//
// Callers have to tell the two apart: a limit that says no is the answer and
// belongs in front of the person, while a database that could not be read is not
// an answer at all and must not be turned into one. Without a sentinel every
// caller was left comparing message text, or treating both the same.
var ErrExceeded = errors.New("quota exceeded")

// Exceeded carries the sentence somebody reads. It answers errors.Is for
// ErrExceeded without putting the sentinel's own words in front of the message —
// err.Error() is printed by more places than the one that classifies it, and
// "quota exceeded: 사용자 Runtime Quota…" is the sentinel leaking into the console.
type Exceeded struct{ Message string }

func (e Exceeded) Error() string { return e.Message }

func (e Exceeded) Is(target error) bool { return target == ErrExceeded }

func exceeded(format string, args ...any) error {
	return Exceeded{Message: fmt.Sprintf(format, args...)}
}

// Who a limit applies to.
//
// The platform had one set of limits for everybody, which answers "how much may
// a person hold" and nothing else. Two questions were missing. A department has
// a budget of its own — the capacity somebody paid for — and it is exceeded by
// the department as a whole rather than by any one member. And a person
// occasionally needs different limits from their colleagues, which until now
// meant raising the limit for everyone.
const (
	ScopeUser       = "user"
	ScopeDepartment = "department"
)

// Limits is what one scope may hold and spend. Zero means "not set here", which
// is what makes inheritance work: a department sets what it cares about, a
// person overrides one field, and everything else falls through to the platform
// default. Unlimited is expressed the same way at the platform level, which is
// how a deployment that never configures quotas keeps behaving as it did.
type Limits struct {
	// What may be held at once.
	MaxRuntimes  int `json:"maxRuntimes,omitempty"`
	MaxCPUMillis int `json:"maxCpuMillis,omitempty"`
	MaxMemoryMB  int `json:"maxMemoryMb,omitempty"`
	MaxStorageGB int `json:"maxStorageGb,omitempty"`
	// MaxGPUs is the scarcest of these by far. It joined the family when a
	// profile's GPU count started reaching the Pod: until then nothing was
	// granted, so nothing needed limiting, and a limit on CPU beside no limit at
	// all on GPUs is the asymmetry that lets one person hold every card.
	MaxGPUs int `json:"maxGpus,omitempty"`
	// What may be run and spent, over the same window the usage report shows.
	MaxRunningTasks int     `json:"maxRunningTasks,omitempty"`
	TokenBudget     int64   `json:"tokenBudget,omitempty"`
	CostBudget      float64 `json:"costBudget,omitempty"`
}

// Empty reports whether anything is set, so the console can tell "inherits
// everything" from "limited to zero" — which is not a thing anybody means.
func (l Limits) Empty() bool { return l == Limits{} }

// Department is a department's own quota, which is two different things and was
// worth naming separately rather than guessing which one an administrator meant.
//
// PerMember is the default for each of its members, overridable per person.
// Total is what the department may hold and spend altogether, no matter how it
// is divided: it is the capacity that was actually bought.
type Department struct {
	PerMember Limits `json:"perMember"`
	Total     Limits `json:"total"`
}

// Resolve combines the levels into the limits that apply to one person.
//
// It is field by field rather than whole-object, because that is what an
// administrator means by "the platform allows 4 runtimes, our department 8, and
// this one person 16": the fields nobody overrode should keep falling through
// rather than reverting to unlimited. Later arguments win, so the order is
// platform, department, person.
func Resolve(levels ...Limits) Limits {
	var out Limits
	for _, level := range levels {
		if level.MaxRuntimes > 0 {
			out.MaxRuntimes = level.MaxRuntimes
		}
		if level.MaxCPUMillis > 0 {
			out.MaxCPUMillis = level.MaxCPUMillis
		}
		if level.MaxMemoryMB > 0 {
			out.MaxMemoryMB = level.MaxMemoryMB
		}
		if level.MaxStorageGB > 0 {
			out.MaxStorageGB = level.MaxStorageGB
		}
		// GPUs fall through like every other dimension. Left out of this loop —
		// which is how they shipped — the field was still stored, still shown on
		// the settings screen and still checked by CheckHeld, but the limits that
		// reached CheckHeld came from here, so it was always zero: unlimited. The
		// one dimension nobody can buy more of overnight was the one with no
		// ceiling, while every other number on the screen said the quota was
		// working.
		if level.MaxGPUs > 0 {
			out.MaxGPUs = level.MaxGPUs
		}
		if level.MaxRunningTasks > 0 {
			out.MaxRunningTasks = level.MaxRunningTasks
		}
		if level.TokenBudget > 0 {
			out.TokenBudget = level.TokenBudget
		}
		if level.CostBudget > 0 {
			out.CostBudget = level.CostBudget
		}
	}
	return out
}

// Held is what a scope is holding right now — one person's, or a whole
// department's added up.
type Held struct {
	Runtimes  int `json:"runtimes"`
	CPUMillis int `json:"cpuMillis"`
	MemoryMB  int `json:"memoryMb"`
	StorageGB int `json:"storageGb"`
	GPUs      int `json:"gpus"`
}

// CheckHeld reports why one more runtime of this size cannot be started, or nil.
//
// The message names the scope, because "Runtime Quota를 초과합니다" with a
// department's limit in it and a personal limit in mind is how somebody spends an
// afternoon raising the wrong number.
func CheckHeld(scope string, limits Limits, held Held, addCPU, addMemory, addGPUs int) error {
	who := scopeName(scope)
	if limits.MaxRuntimes > 0 && held.Runtimes+1 > limits.MaxRuntimes {
		return exceeded("%s Runtime Quota(%d개)를 초과합니다", who, limits.MaxRuntimes)
	}
	if limits.MaxCPUMillis > 0 && held.CPUMillis+addCPU > limits.MaxCPUMillis {
		return exceeded("%s CPU Quota(%dm)를 초과합니다", who, limits.MaxCPUMillis)
	}
	if limits.MaxMemoryMB > 0 && held.MemoryMB+addMemory > limits.MaxMemoryMB {
		return exceeded("%s Memory Quota(%dMB)를 초과합니다", who, limits.MaxMemoryMB)
	}
	if limits.MaxGPUs > 0 && held.GPUs+addGPUs > limits.MaxGPUs {
		return exceeded("%s GPU Quota(%d개)를 초과합니다", who, limits.MaxGPUs)
	}
	return nil
}

// CheckStorage reports why one more workspace of this size cannot be created.
func CheckStorage(scope string, limits Limits, usedGB, addGB int) error {
	if limits.MaxStorageGB > 0 && usedGB+addGB > limits.MaxStorageGB {
		// exceeded, like every other refusal here. This one was a bare error, so a
		// caller asking "was this a limit saying no, or a failure to evaluate one"
		// — the question this package's sentinel exists for — got the wrong answer
		// for storage alone, and turned a refusal into an internal failure.
		return exceeded("%s Storage Quota(%dGB)를 초과합니다", scopeName(scope), limits.MaxStorageGB)
	}
	return nil
}

func scopeName(scope string) string {
	if scope == ScopeDepartment {
		return "부서"
	}
	return "사용자"
}

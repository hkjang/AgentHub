package api

import (
	"reflect"
	"strings"
	"testing"

	"github.com/hkjang/AgentHub/internal/quota"
)

// quotaComplaint is written dimension by dimension, and a dimension left out of
// it is not rejected — it is accepted and then read by CheckHeld, which asks
// only whether the limit is greater than zero. A negative GPU count therefore
// stored cleanly and meant unlimited, on a screen that showed the number
// somebody had typed. GPUs were the dimension that was left out.
func TestEveryLimitIsCheckedForANegativeValue(t *testing.T) {
	limits := reflect.TypeOf(quota.Limits{})
	for index := 0; index < limits.NumField(); index++ {
		field := limits.Field(index)
		level := reflect.New(limits).Elem()
		switch value := level.Field(index); value.Kind() {
		case reflect.Int, reflect.Int64:
			value.SetInt(-1)
		case reflect.Float64:
			value.SetFloat(-1)
		default:
			t.Fatalf("%s is a %s, which this sweep does not know how to set", field.Name, value.Kind())
		}
		if complaint := quotaComplaint(level.Interface().(quota.Limits)); complaint == "" {
			t.Errorf("%s = -1 was accepted; a negative limit is a typo that reads as unlimited", field.Name)
		}
	}
}

// Zero is not a complaint anywhere: it is how a level says "inherit", and a
// deployment that configures nothing has to keep saving.
func TestAnUnsetQuotaIsNotAComplaint(t *testing.T) {
	if complaint := quotaComplaint(quota.Limits{}); complaint != "" {
		t.Errorf("limits that set nothing were refused: %s", complaint)
	}
}

// A unit mistake removes the limit it was meant to set, so the absurd values are
// refused with the unit named. GPUs are counted in cards, and a four-digit one
// is somebody who typed the CPU number into the wrong box.
func TestAnAbsurdGPUCountIsRefusedWithItsUnit(t *testing.T) {
	complaint := quotaComplaint(quota.Limits{MaxGPUs: 100_000})
	if complaint == "" {
		t.Fatal("a hundred thousand GPUs was accepted as a limit")
	}
	if !strings.Contains(complaint, "GPU") {
		t.Errorf("the complaint does not say which limit it is about: %s", complaint)
	}
	if complaint := quotaComplaint(quota.Limits{MaxGPUs: 8}); complaint != "" {
		t.Errorf("an ordinary GPU limit was refused: %s", complaint)
	}
}

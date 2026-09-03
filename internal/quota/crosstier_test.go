package quota

import (
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// The console draws the quota screens from its own list of limits, and a
// dimension missing from that list has no row: it cannot be set on a department
// or a person, it does not appear under "실제 적용되는 한도", and the personal
// panel does not show it being used. Nothing looks broken — the screen shows
// every limit it knows about, and reads as though the missing one were
// unlimited.
//
// GPUs shipped that way in all three places at once: absent from Resolve, from
// the API's validation, and from this list. The first two have their own tests;
// this is the one that keeps the console from drifting a dimension behind the
// server again.
func TestTheConsoleDrawsEveryLimitThisPackageDefines(t *testing.T) {
	body, err := os.ReadFile(filepath.Join("..", "..", "web", "src", "quota.ts"))
	if err != nil {
		t.Skipf("console source is not present in this checkout: %v", err)
	}
	drawn := map[string]bool{}
	for _, entry := range regexp.MustCompile(`\{\s*key:\s*'([A-Za-z]+)'`).FindAllStringSubmatch(string(body), -1) {
		drawn[entry[1]] = true
	}
	if len(drawn) == 0 {
		t.Fatal("no limit fields were found in the console's list; this test is reading the wrong shape")
	}
	limits := reflect.TypeOf(Limits{})
	for index := 0; index < limits.NumField(); index++ {
		field := limits.Field(index)
		name := strings.Split(field.Tag.Get("json"), ",")[0]
		if name == "" || name == "-" {
			continue
		}
		if !drawn[name] {
			t.Errorf("%s (%s) has no row in the console's LIMIT_FIELDS, so no screen can show or set it", field.Name, name)
		}
		delete(drawn, name)
	}
	for name := range drawn {
		t.Errorf("the console draws a limit %q this package does not define", name)
	}
}

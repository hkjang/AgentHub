package api

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"
	"time"
)

// Guessing passwords at a running deployment.
//
// Before the throttle, sixty wrong passwords went through this route in 2.4
// seconds and the right one still opened the account on the next attempt. There
// was nothing to read in the code, because there was nothing there — which is
// why this asks the deployment rather than the source.
//
//	AGENTHUB_TEST_URL=http://localhost:18080 AGENTHUB_TEST_USER=... \
//	AGENTHUB_TEST_PASSWORD=... go test ./internal/api/ -run GuessingStops -v
func TestGuessingStopsAtTheDoor(t *testing.T) {
	base := os.Getenv("AGENTHUB_TEST_URL")
	if base == "" {
		t.Skip("set AGENTHUB_TEST_URL to check the login route against a running deployment")
	}
	// A name nobody has: the answer must be the same as for a real one, and this
	// leaves the deployment's own accounts untouched.
	name := "nobody-guess-probe"
	client := &apiClient{base: base, http: &http.Client{Timeout: 30 * time.Second}}

	refused, unauthorized, retryAfter := 0, 0, ""
	for i := 0; i < 60; i++ {
		payload, _ := json.Marshal(map[string]string{"username": name, "password": "wrong-password"})
		request, _ := http.NewRequest(http.MethodPost, base+"/api/v1/auth/login", strings.NewReader(string(payload)))
		request.Header.Set("content-type", "application/json")
		response, err := client.http.Do(request)
		if err != nil {
			t.Fatalf("attempt %d could not be made: %v", i+1, err)
		}
		response.Body.Close()
		switch response.StatusCode {
		case http.StatusTooManyRequests:
			refused++
			if retryAfter == "" {
				retryAfter = response.Header.Get("Retry-After")
			}
		case http.StatusUnauthorized:
			unauthorized++
		default:
			t.Fatalf("attempt %d answered %d, which is neither a refusal nor a rejection", i+1, response.StatusCode)
		}
	}
	if refused == 0 {
		t.Fatalf("sixty wrong passwords, %d rejected and none refused: the door counts nothing", unauthorized)
	}
	if unauthorized > loginFailsPerSource {
		t.Errorf("%d guesses were answered before anything stopped, past the source allowance of %d", unauthorized, loginFailsPerSource)
	}
	if retryAfter == "" {
		t.Error("the refusal carries no Retry-After, so a person who mistyped cannot tell the door from a fault")
	}
	t.Logf("60 guesses: %d answered, %d refused, Retry-After: %ss", unauthorized, refused, retryAfter)

	// The account whose name was never guessed at is unaffected — a throttle that
	// takes the deployment down with the attacker is not a defence.
	loginAs(t, base, os.Getenv("AGENTHUB_TEST_USER"), os.Getenv("AGENTHUB_TEST_PASSWORD"))
}

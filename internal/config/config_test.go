package config

import (
	"encoding/base64"
	"testing"
)

func TestDecodeKey(t *testing.T) {
	want := []byte("01234567890123456789012345678901")
	for _, value := range []string{string(want), base64.StdEncoding.EncodeToString(want)} {
		got, err := decodeKey(value)
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != string(want) {
			t.Fatalf("decoded key mismatch: %q", got)
		}
	}
	if _, err := decodeKey("short"); err == nil {
		t.Fatal("expected invalid key to fail")
	}
}

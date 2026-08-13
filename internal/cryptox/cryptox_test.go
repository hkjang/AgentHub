package cryptox

import "testing"

func TestRoundTripAndContextBinding(t *testing.T) {
	c, err := New([]byte("01234567890123456789012345678901"))
	if err != nil {
		t.Fatal(err)
	}
	value, err := c.Encrypt([]byte("secret"), "user:123")
	if err != nil {
		t.Fatal(err)
	}
	plain, err := c.Decrypt(value, "user:123")
	if err != nil || string(plain) != "secret" {
		t.Fatalf("round trip failed: %q %v", plain, err)
	}
	if _, err := c.Decrypt(value, "user:456"); err == nil {
		t.Fatal("context change must invalidate ciphertext")
	}
}

package config

import (
	"bytes"
	"encoding/hex"
	"testing"
)

func TestParseCreditsEncKey(t *testing.T) {
	key := bytes.Repeat([]byte{0xab}, 32)
	hexKey := hex.EncodeToString(key)

	if got := parseCreditsEncKey(hexKey); !bytes.Equal(got, key) {
		t.Fatalf("hex key = %x, want %x", got, key)
	}
	if got := parseCreditsEncKey(string(key)); !bytes.Equal(got, key) {
		t.Fatalf("raw key = %x, want %x", got, key)
	}
}

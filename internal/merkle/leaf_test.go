package merkle

import (
	"bytes"
	"testing"
	"time"
)

func TestLeafBytesGolden(t *testing.T) {
	at := time.Date(2026, 7, 15, 10, 30, 0, 123456000, time.UTC)
	digest := bytes.Repeat([]byte{0xab}, 32)
	got := LeafBytes("record", "flywheel", "participants", "bap.example.com", 2, digest, "seed", at)
	want := `["dedi/v1/leaf","record","flywheel","participants","bap.example.com",2,` +
		`"abababababababababababababababababababababababababababababababab","seed",` +
		`"2026-07-15T10:30:00.123456Z"]`
	if string(got) != want {
		t.Fatalf("golden mismatch:\n got:  %s\n want: %s", got, want)
	}
}

func TestLeafBytesFieldSensitivity(t *testing.T) {
	at := time.Now().UTC()
	digest := bytes.Repeat([]byte{1}, 32)
	base := LeafBytes("record", "ns", "reg", "rec", 1, digest, "u", at)
	variants := [][]byte{
		LeafBytes("registry", "ns", "reg", "rec", 1, digest, "u", at),
		LeafBytes("record", "ns2", "reg", "rec", 1, digest, "u", at),
		LeafBytes("record", "ns", "reg", "rec", 2, digest, "u", at),
		LeafBytes("record", "ns", "reg", "rec", 1, bytes.Repeat([]byte{2}, 32), "u", at),
		LeafBytes("record", "ns", "reg", "rec", 1, digest, "u", at.Add(time.Microsecond)),
	}
	for i, v := range variants {
		if bytes.Equal(base, v) {
			t.Fatalf("variant %d did not change leaf bytes", i)
		}
	}
	if !bytes.Equal(base, LeafBytes("record", "ns", "reg", "rec", 1, digest, "u", at)) {
		t.Fatal("same inputs produced different leaf bytes")
	}
}

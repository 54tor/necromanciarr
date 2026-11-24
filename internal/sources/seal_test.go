package sources

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"strings"
	"testing"
)

// testKey est une clé de test à faible entropie, jamais une vraie clé.
var testKey = bytes.Repeat([]byte{0x01}, KeySize)

const testInfohash = "0123456789abcdef0123456789abcdef01234567"

func testBinding() Binding {
	return Binding{Infohash: testInfohash, Tracker: "tracker-a", Source: "torznab:12"}
}

func newTestSealer(t *testing.T, key []byte) *Sealer {
	t.Helper()
	sealer, err := NewSealer(key)
	if err != nil {
		t.Fatalf("NewSealer: %v", err)
	}
	return sealer
}

func sealTest(t *testing.T, sealer *Sealer, plain []byte, binding Binding) []byte {
	t.Helper()
	blob, err := sealer.Seal(plain, binding)
	if err != nil {
		t.Fatalf("Seal: %v", err)
	}
	return blob
}

func TestSeal_roundTrip(t *testing.T) {
	sealer := newTestSealer(t, testKey)
	expectedValue := indexerTorrent()

	blob := sealTest(t, sealer, expectedValue, testBinding())
	gotValue, err := sealer.Unseal(blob, testBinding())

	if err != nil || !bytes.Equal(gotValue, expectedValue) {
		t.Fatalf("gotValue %q, %v, expectedValue the original torrent", gotValue, err)
	}
	if blob[0] != formatVersion || len(blob) != 1+nonceSize+len(expectedValue)+tagSize {
		t.Fatalf("gotValue blob of %d bytes starting with %d, expectedValue version‖nonce‖ciphertext‖tag", len(blob), blob[0])
	}
	if bytes.Contains(blob, []byte(fakePasskey)) || bytes.Contains(blob, []byte("tracker.example.com")) {
		t.Fatal("gotValue the plaintext in the sealed blob")
	}
}

func TestSeal_refusesBlobMovedToAnotherCandidate(t *testing.T) {
	sealer := newTestSealer(t, testKey)
	blob := sealTest(t, sealer, indexerTorrent(), testBinding())
	moved := map[string]func(*Binding){
		"other infohash": func(binding *Binding) { binding.Infohash = strings.Repeat("f", 40) },
		"other tracker":  func(binding *Binding) { binding.Tracker = "tracker-b" },
		"empty tracker":  func(binding *Binding) { binding.Tracker = "" },
		"other source":   func(binding *Binding) { binding.Source = "torznab:7" },
		"provider":       func(binding *Binding) { binding.Source = "provider" },
		"shifted fields": func(binding *Binding) { binding.Tracker, binding.Source = "tracker-a|torznab:12", "" },
	}
	for name, change := range moved {
		t.Run(name, func(t *testing.T) {
			binding := testBinding()
			change(&binding)

			plain, err := sealer.Unseal(blob, binding)

			if !errors.Is(err, ErrSealedUnreadable) || plain != nil {
				t.Fatalf("gotValue %q, %v, expectedValue ErrSealedUnreadable", plain, err)
			}
		})
	}
}

func TestSeal_refusesWrongKey(t *testing.T) {
	blob := sealTest(t, newTestSealer(t, testKey), indexerTorrent(), testBinding())
	otherKey := bytes.Repeat([]byte{0x02}, KeySize)

	plain, err := newTestSealer(t, otherKey).Unseal(blob, testBinding())

	if !errors.Is(err, ErrSealedUnreadable) || plain != nil {
		t.Fatalf("gotValue %q, %v, expectedValue ErrSealedUnreadable", plain, err)
	}
}

func TestSeal_refusesTruncatedOrFlippedBlob(t *testing.T) {
	sealer := newTestSealer(t, testKey)
	blob := sealTest(t, sealer, []byte("d4:info"+infoDictionary+"e"), testBinding())
	expectedText := ErrSealedUnreadable.Error()
	for length := range len(blob) {
		if _, err := sealer.Unseal(blob[:length], testBinding()); !errors.Is(err, ErrSealedUnreadable) || err.Error() != expectedText {
			t.Fatalf("truncated to %d: gotValue %v, expectedValue the single opaque error", length, err)
		}
	}
	for position := range len(blob) {
		flipped := bytes.Clone(blob)
		flipped[position] ^= 0x01
		if _, err := sealer.Unseal(flipped, testBinding()); !errors.Is(err, ErrSealedUnreadable) || err.Error() != expectedText {
			t.Fatalf("bit flipped at %d: gotValue %v, expectedValue the single opaque error", position, err)
		}
	}
	if _, err := sealer.Unseal(append(bytes.Clone(blob), 0), testBinding()); !errors.Is(err, ErrSealedUnreadable) {
		t.Fatalf("extended blob: gotValue %v, expectedValue ErrSealedUnreadable", err)
	}
}

func TestSeal_usesAFreshNonceEachTime(t *testing.T) {
	sealer := newTestSealer(t, testKey)
	plain := []byte("same plaintext")
	const sealCount = 2000
	seenNonces := make(map[string]bool, sealCount)
	seenBlobs := make(map[string]bool, sealCount)
	for range sealCount {
		blob := sealTest(t, sealer, plain, testBinding())
		nonce := string(blob[1 : 1+nonceSize])
		if seenNonces[nonce] || seenBlobs[string(blob)] {
			t.Fatalf("gotValue a reused nonce or identical blob after %d seals", len(seenNonces))
		}
		if nonce == string(make([]byte, nonceSize)) {
			t.Fatal("gotValue an all-zero nonce")
		}
		seenNonces[nonce], seenBlobs[string(blob)] = true, true
	}
}

type failingReader struct{ served int }

func (reader *failingReader) Read(buffer []byte) (int, error) {
	if reader.served > 0 {
		return 0, io.ErrUnexpectedEOF
	}
	reader.served = 1
	buffer[0] = 0x07
	return 1, nil
}

func TestSeal_failingRandomSourceIsAnError(t *testing.T) {
	sealer := newTestSealer(t, testKey)
	originalSource := nonceSource
	t.Cleanup(func() { nonceSource = originalSource })
	nonceSource = &failingReader{}

	blob, err := sealer.Seal([]byte("plain"), testBinding())

	if err == nil || blob != nil || errors.Is(err, ErrSealedUnreadable) {
		t.Fatalf("gotValue %q, %v, expectedValue a nonce error and no blob", blob, err)
	}
}

func TestSeal_bindingWithDelimiterOrControlCannotCollide(t *testing.T) {
	sealer := newTestSealer(t, testKey)
	first := Binding{Infohash: testInfohash, Tracker: "a|b", Source: "c"}
	second := Binding{Infohash: testInfohash, Tracker: "a", Source: "b|c"}
	invalid := []Binding{
		first, second,
		{Infohash: testInfohash, Tracker: "a\nb", Source: "c"},
		{Infohash: testInfohash, Tracker: "a", Source: "c\x00"},
		{Infohash: testInfohash, Tracker: "a", Source: ""},
		{Infohash: testInfohash, Tracker: "\xff", Source: "c"},
		{Infohash: strings.ToUpper(testInfohash), Tracker: "a", Source: "c"},
		{Infohash: testInfohash[:39], Tracker: "a", Source: "c"},
		{Infohash: testInfohash + "|", Tracker: "a", Source: "c"},
	}
	for _, binding := range invalid {
		if blob, err := sealer.Seal([]byte("plain"), binding); !errors.Is(err, ErrInvalidBinding) || blob != nil {
			t.Fatalf("Seal with %+v: gotValue %v, expectedValue ErrInvalidBinding", binding, err)
		}
	}
	blob := sealTest(t, sealer, []byte("plain"), Binding{Infohash: testInfohash, Tracker: "a", Source: "b"})
	if _, err := sealer.Unseal(blob, Binding{Infohash: testInfohash, Tracker: "a|b", Source: ""}); !errors.Is(err, ErrSealedUnreadable) {
		t.Fatalf("Unseal with a delimiter: gotValue %v, expectedValue ErrSealedUnreadable", err)
	}
}

func TestSeal_associatedDataIsExactlyInfohashTrackerSource(t *testing.T) {
	gotValue, err := associatedData(testBinding())

	expectedValue := testInfohash + "|tracker-a|torznab:12"
	if err != nil || string(gotValue) != expectedValue {
		t.Fatalf("gotValue %q, %v, expectedValue %q", gotValue, err, expectedValue)
	}
}

func TestNewSealer_refusesAKeyOfTheWrongLength(t *testing.T) {
	for _, length := range []int{0, 16, 24, 31, 33, 64} {
		key := bytes.Repeat([]byte{'k'}, length)

		sealer, err := NewSealer(key)

		if !errors.Is(err, ErrInvalidKey) || sealer != nil || strings.Contains(err.Error(), "kkkk") {
			t.Fatalf("length %d: gotValue %v, expectedValue ErrInvalidKey without the key", length, err)
		}
	}
}

func TestSealer_rendersNothingOfTheKey(t *testing.T) {
	sealer := newTestSealer(t, testKey)
	var logged bytes.Buffer
	slog.New(slog.NewTextHandler(&logged, nil)).Info("sealer", "value", sealer)
	renderings := []string{
		fmt.Sprint(sealer), fmt.Sprintf("%+v", sealer), fmt.Sprintf("%#v", sealer), fmt.Sprintf("%s", sealer),
		fmt.Sprintf("%x", sealer), fmt.Sprintf("%v", []*Sealer{sealer}),
	}
	for _, rendering := range renderings {
		if strings.ReplaceAll(strings.Trim(rendering, "[]"), redactedSealer, "") != "" {
			t.Fatalf("gotValue %q, expectedValue only %q", rendering, redactedSealer)
		}
	}
	if !strings.Contains(logged.String(), redactedSealer) || strings.Count(logged.String(), "{") != strings.Count(redactedSealer, "{") {
		t.Fatalf("gotValue log %q, expectedValue only %q", logged.String(), redactedSealer)
	}
}

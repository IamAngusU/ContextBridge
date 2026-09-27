package cluster

import (
	"errors"
	"strings"
	"testing"
)

type failingIDReader struct{}

func (failingIDReader) Read([]byte) (int, error) {
	return 0, errors.New("entropy unavailable")
}

func testRandomID(t testing.TB, prefix string) string {
	t.Helper()
	id, err := randomID(prefix)
	if err != nil {
		t.Fatal(err)
	}
	return id
}

func TestRandomIDFailsClosedWithoutSecureEntropy(t *testing.T) {
	id, err := randomIDFromReader("job", failingIDReader{})
	if err == nil || id != "" || !strings.Contains(err.Error(), "secure job id") {
		t.Fatalf("insecure fallback remained: id=%q err=%v", id, err)
	}
}

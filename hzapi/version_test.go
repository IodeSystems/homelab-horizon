package hzapi

import (
	"errors"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
)

func TestCheckAcceptsTheServedRange(t *testing.T) {
	for v := MinVersion; v <= Version; v++ {
		if err := Check(v); err != nil {
			t.Fatalf("Check(%d) inside [%d,%d] = %v, want nil", v, MinVersion, Version, err)
		}
	}
}

// A client older than the floor is refused, and the refusal names both sides.
// "400 Bad Request" alone sends an operator to read source.
func TestCheckRefusesAnOlderClientAndSaysSo(t *testing.T) {
	err := Check(MinVersion - 1)
	var m *Mismatch
	if !errors.As(err, &m) {
		t.Fatalf("Check(too old) = %v, want a *Mismatch", err)
	}
	if !m.TooOld {
		t.Fatal("a client below MinVersion is not marked TooOld")
	}
	msg := m.Error()
	for _, want := range []string{"rebuild the client"} {
		if !strings.Contains(msg, want) {
			t.Errorf("message %q does not contain %q", msg, want)
		}
	}
	if !strings.Contains(msg, strconv.Itoa(MinVersion-1)) || !strings.Contains(msg, strconv.Itoa(Version)) {
		t.Errorf("message %q does not name both the client's version and the server's", msg)
	}
}

// A client NEWER than the server is refused too. Serving it and hoping is how a
// consumer meets a missing field as a nil dereference in production rather than
// as a refusal on its first call.
func TestCheckRefusesANewerClient(t *testing.T) {
	err := Check(Version + 1)
	var m *Mismatch
	if !errors.As(err, &m) {
		t.Fatalf("Check(too new) = %v, want a *Mismatch", err)
	}
	if m.TooOld {
		t.Fatal("a client above Version is marked TooOld")
	}
	if !strings.Contains(m.Error(), "upgrade hz") {
		t.Errorf("message %q does not tell the operator which side to move", m.Error())
	}
}

// Absent and present-but-garbage are different. A legacy client sends nothing
// and is currently served; a client sending nonsense has a bug and must not be
// mistaken for a legacy one.
func TestFromRequestSeparatesAbsentFromMalformed(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/whatever", nil)
	if _, ok := FromRequest(r); ok {
		t.Fatal("an absent header reported as declared")
	}

	r.Header.Set(HeaderVersion, "not-a-number")
	v, ok := FromRequest(r)
	if !ok {
		t.Fatal("a malformed header reported as absent; it would be served as legacy")
	}
	if err := Check(v); err == nil {
		t.Fatal("a malformed version passed Check")
	}
}

func TestAdvertiseCarriesBothNumbers(t *testing.T) {
	h := http.Header{}
	Advertise(h)
	if h.Get(HeaderVersion) == "" || h.Get(HeaderMin) == "" {
		t.Fatalf("Advertise set %v, want both a current and a minimum", h)
	}
}

// SetRequest and FromRequest must agree, or a client declares something the
// server cannot read and silently falls into the legacy path.
func TestSetRequestRoundTrips(t *testing.T) {
	r := httptest.NewRequest(http.MethodGet, "/api/v1/whatever", nil)
	SetRequest(r.Header)
	v, ok := FromRequest(r)
	if !ok || v != Version {
		t.Fatalf("round trip gave (%d, %v), want (%d, true)", v, ok, Version)
	}
}

// MinVersion above Version would refuse every client including this build's own.
func TestTheServedRangeIsNotEmpty(t *testing.T) {
	if MinVersion > Version {
		t.Fatalf("MinVersion %d > Version %d: no client can satisfy this", MinVersion, Version)
	}
}

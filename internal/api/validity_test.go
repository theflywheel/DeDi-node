package api

import (
	"testing"
	"time"
)

func TestValidityWindow(t *testing.T) {
	now := time.Date(2026, 6, 1, 0, 0, 0, 0, time.UTC)
	b := func(p *bool) string {
		if p == nil {
			return "absent"
		}
		if *p {
			return "true"
		}
		return "false"
	}
	cases := []struct {
		name             string
		payload          string
		wantTill         string // "" means nil
		wantExp, wantNot string
	}{
		{name: "no window", payload: `{"subscriber_id":"a"}`, wantExp: "absent", wantNot: "absent"},
		{name: "still valid", payload: `{"valid_until":"2027-01-01T00:00:00Z"}`,
			wantTill: "2027-01-01T00:00:00Z", wantExp: "false", wantNot: "absent"},
		{name: "expired", payload: `{"valid_until":"2025-01-01T00:00:00Z"}`,
			wantTill: "2025-01-01T00:00:00Z", wantExp: "true", wantNot: "absent"},
		{name: "not yet valid", payload: `{"valid_from":"2026-12-01T00:00:00Z"}`,
			wantExp: "absent", wantNot: "true"},
		{name: "already begun", payload: `{"valid_from":"2026-01-01T00:00:00Z"}`,
			wantExp: "absent", wantNot: "false"},
		{name: "both, in window", payload: `{"valid_from":"2026-01-01T00:00:00Z","valid_until":"2027-01-01T00:00:00Z"}`,
			wantTill: "2027-01-01T00:00:00Z", wantExp: "false", wantNot: "false"},
		// A malformed timestamp must not break the read plane.
		{name: "garbage timestamps", payload: `{"valid_from":"yesterday","valid_until":"soon"}`,
			wantExp: "absent", wantNot: "absent"},
		{name: "payload not an object", payload: `"nope"`, wantExp: "absent", wantNot: "absent"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			till, exp, notYet := validityWindow([]byte(tc.payload), now)
			gotTill := ""
			if till != nil {
				gotTill = *till
			}
			if gotTill != tc.wantTill {
				t.Fatalf("valid_till = %q, want %q", gotTill, tc.wantTill)
			}
			if b(exp) != tc.wantExp {
				t.Fatalf("expired = %s, want %s", b(exp), tc.wantExp)
			}
			if b(notYet) != tc.wantNot {
				t.Fatalf("not_yet_valid = %s, want %s", b(notYet), tc.wantNot)
			}
		})
	}
}

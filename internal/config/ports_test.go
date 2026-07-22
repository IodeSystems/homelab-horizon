package config

import "testing"

func TestPortExcluded(t *testing.T) {
	cfg := &Config{
		PortExclusions: []PortRange{
			{From: 7000, To: 7099, Note: "custom band"},
			{From: 12345},
		},
	}
	cases := []struct {
		port int
		want bool
		why  string
	}{
		{8000, true, "builtin 8xxx range"},
		{8050, true, "builtin 8xxx range mid"},
		{3000, true, "builtin node dev"},
		{5432, true, "builtin postgres"},
		{22, true, "builtin privileged"},
		{7050, true, "custom band"},
		{12345, true, "custom single"},
		{20000, false, "safe band, not excluded"},
		{25000, false, "safe band, not excluded"},
		{7100, false, "just above custom band"},
	}
	for _, c := range cases {
		if got := cfg.PortExcluded(c.port); got != c.want {
			t.Errorf("PortExcluded(%d) = %v, want %v (%s)", c.port, got, c.want, c.why)
		}
	}
}

func TestPortRangeContains(t *testing.T) {
	single := PortRange{From: 9000}
	if !single.Contains(9000) || single.Contains(9001) {
		t.Error("single-port range (To==0) should match only From")
	}
	span := PortRange{From: 8000, To: 8099}
	if !span.Contains(8000) || !span.Contains(8099) || span.Contains(8100) {
		t.Error("span bounds wrong")
	}
}

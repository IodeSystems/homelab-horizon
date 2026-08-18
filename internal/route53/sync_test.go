package route53

import "testing"

// The published TTL is half of how long a client keeps dialling a WAN address
// that has moved. Comparing only the values made lowering it a no-op until the
// address changed anyway — so the shorter TTL could only ever arrive after the
// moment it existed to shorten.
func TestNeedsUpdate(t *testing.T) {
	tests := []struct {
		name    string
		current *currentRecordSet
		values  []string
		ttl     int
		want    bool
	}{
		{
			name:    "ttl lowered, value unchanged",
			current: &currentRecordSet{TTL: 300, Values: []string{"1.2.3.4"}},
			values:  []string{"1.2.3.4"},
			ttl:     60,
			want:    true,
		},
		{
			name:    "nothing changed",
			current: &currentRecordSet{TTL: 60, Values: []string{"1.2.3.4"}},
			values:  []string{"1.2.3.4"},
			ttl:     60,
		},
		{
			name:    "no ttl asked for",
			current: &currentRecordSet{TTL: 300, Values: []string{"1.2.3.4"}},
			values:  []string{"1.2.3.4"},
			ttl:     0,
		},
		{
			name:    "value changed",
			current: &currentRecordSet{TTL: 60, Values: []string{"1.2.3.4"}},
			values:  []string{"9.9.9.9"},
			ttl:     60,
			want:    true,
		},
		{
			name:    "round-robin set reordered is not a change",
			current: &currentRecordSet{TTL: 60, Values: []string{"1.2.3.4", "5.6.7.8"}},
			values:  []string{"5.6.7.8", "1.2.3.4"},
			ttl:     60,
		},
		{
			name:   "record absent",
			values: []string{"1.2.3.4"},
			ttl:    60,
			want:   true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := needsUpdate(tt.current, tt.values, tt.ttl); got != tt.want {
				t.Errorf("needsUpdate() = %v, want %v", got, tt.want)
			}
		})
	}
}

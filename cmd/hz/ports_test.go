package main

import (
	"reflect"
	"testing"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

func TestIsCommonPort(t *testing.T) {
	common := []int{80, 443, 22, 3306, 5432, 6379, 4222, 27017, 9200, 5672,
		8080, 8000, 8443, 9090, 8500, 3000, 5000, // explicit
		8050, 8099, 3005, 5010, 9042, 9099} // range members / db
	for _, p := range common {
		if !isCommonPort(p) {
			t.Errorf("isCommonPort(%d) = false, want true", p)
		}
	}
	safe := []int{20000, 20001, 21000, 25000, 30000, 32767, 19999, 11000}
	for _, p := range safe {
		if isCommonPort(p) {
			t.Errorf("isCommonPort(%d) = true, want false", p)
		}
	}
}

func TestFindFreeRangeSkipsUsedAndCommon(t *testing.T) {
	tests := []struct {
		name  string
		used  map[int]bool
		from  int
		count int
		want  int
	}{
		{"first free in safe band", map[int]bool{}, 20000, 1, 20000},
		{"skips a used port", map[int]bool{20000: true}, 20000, 1, 20001},
		{"skips common port when from lands on it", map[int]bool{}, 8080, 1, 8100},
		{"contiguous range hops over conflict", map[int]bool{20002: true}, 20000, 3, 20003},
		{"range avoids common band", map[int]bool{}, 8098, 3, 8100},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := findFreeRange(tt.used, tt.from, tt.count, isCommonPort); got != tt.want {
				t.Errorf("findFreeRange(from=%d,count=%d) = %d, want %d", tt.from, tt.count, got, tt.want)
			}
		})
	}
}

func TestSuggestFree(t *testing.T) {
	used := map[int]bool{20000: true, 20001: true}
	got := suggestFree(used, 20000, 3, isCommonPort)
	want := []int{20002, 20003, 20004}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("suggestFree = %v, want %v", got, want)
	}
	// from below the safe band is clamped up to safeBandLow.
	if got := suggestFree(map[int]bool{}, 500, 1, isCommonPort); len(got) != 1 || got[0] != safeBandLow {
		t.Errorf("suggestFree(from=500) = %v, want [%d]", got, safeBandLow)
	}
}

func TestUsedTCPExcludesUDP(t *testing.T) {
	pm := apitypes.HostPortMapResponse{Hosts: map[string][]apitypes.HostPortEntry{
		"h": {
			{Port: "8080", Proto: "tcp", Service: "a"},
			{Port: "53", Proto: "udp", Service: "dnsmasq"},
			{Port: "51820", Proto: "udp", Service: "wireguard"},
			{Port: "9000", Proto: "", Service: "legacy-empty-proto"}, // treated as tcp
		},
	}}
	used := usedTCP(pm, "h")
	if !used[8080] || !used[9000] {
		t.Errorf("expected tcp/empty-proto ports marked used: %v", used)
	}
	if used[53] || used[51820] {
		t.Errorf("udp ports must not block tcp allocation: %v", used)
	}
}

package remote

import (
	"testing"
	"time"
)

// TestResolveKeepAlive pins the interval contract: 0 means the default, a negative
// value disables keepalives, and a positive value passes through unchanged. This is
// what config's ssh.keepalive_interval relies on (the live ping/drop behavior is
// covered by the functional reconnect suite).
func TestResolveKeepAlive(t *testing.T) {
	cases := []struct {
		name string
		in   time.Duration
		want time.Duration
	}{
		{"zero defaults to 20s", 0, keepAliveDefault},
		{"negative disables", -1 * time.Second, 0},
		{"positive passes through", 45 * time.Second, 45 * time.Second},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := resolveKeepAlive(c.in); got != c.want {
				t.Errorf("resolveKeepAlive(%v) = %v, want %v", c.in, got, c.want)
			}
		})
	}
}

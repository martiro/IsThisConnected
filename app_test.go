package main

import (
	"testing"
)

func TestValidThresholds(t *testing.T) {
	c := defaultConfig()
	if err := validThresholds(c); err != nil {
		t.Fatalf("defaults rejected: %v", err)
	}
	c.LinkRatePct = 0 // 0 = link speed check off
	if err := validThresholds(c); err != nil {
		t.Errorf("link_rate_pct 0 rejected: %v", err)
	}
	for _, bad := range []func(*Config){
		func(c *Config) { c.LinkRatePct = 150 },
		func(c *Config) { c.RTTGatewayMs = 0 },
		func(c *Config) { c.OpenAfterSec = 0 },
		func(c *Config) { c.LossPct = -1 },
	} {
		c := defaultConfig()
		bad(&c)
		if validThresholds(c) == nil {
			t.Errorf("accepted invalid config %+v", c)
		}
	}
}

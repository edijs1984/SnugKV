package config

import "testing"

func TestFailoverDiscoverySeedsRequireAuth(t *testing.T) {
	c := Default()
	c.FailoverDiscoverySeeds = []string{"127.0.0.1:7001"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected discovery seeds without masterauth to fail")
	}
}

func TestFailoverDiscoverySeedValidation(t *testing.T) {
	c := Default()
	c.MasterAuth = "secret"
	c.FailoverDiscoverySeeds = []string{"not-an-address"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected invalid discovery seed to fail")
	}
}

func TestFailoverDiscoveryConfigValid(t *testing.T) {
	c := Default()
	c.MasterAuth = "secret"
	c.FailoverDiscoverySeeds = []string{"127.0.0.1:7001"}
	c.FailoverDiscoveryIntervalMS = 250
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

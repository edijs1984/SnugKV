package config

import (
	"strings"
	"testing"
)

func TestFailoverConfigEpochRequiresGroupID(t *testing.T) {
	c := Default()
	c.FailoverConfigEpoch = 1
	if err := c.Validate(); err == nil {
		t.Fatal("expected failover_config_epoch without group id to fail")
	}
}

func TestFailoverGroupIDLength(t *testing.T) {
	c := Default()
	c.FailoverGroupID = strings.Repeat("x", 129)
	if err := c.Validate(); err == nil {
		t.Fatal("expected oversized failover_group_id to fail")
	}
}

func TestFailoverMembershipIdentityValid(t *testing.T) {
	c := Default()
	c.FailoverGroupID = "cluster-a"
	c.FailoverConfigEpoch = 42
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

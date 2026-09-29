package config

import "testing"

func TestClusterConfigValid(t *testing.T) {
	c := Default()
	c.ClusterEnabled = true
	c.ClusterNodeAddr = "127.0.0.1:7000"
	c.ClusterControlAuth = "cluster-secret"
	c.ClusterSlots = map[string]string{
		"0-8191":     "127.0.0.1:7000",
		"8192-16383": "127.0.0.1:7001",
	}
	if err := c.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestClusterConfigRequiresNodeAddress(t *testing.T) {
	c := Default()
	c.ClusterEnabled = true
	c.ClusterSlots = map[string]string{"0-16383": "127.0.0.1:7000"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected missing cluster_node_addr to fail")
	}
}

func TestClusterConfigRejectsOverlappingSlots(t *testing.T) {
	c := Default()
	c.ClusterEnabled = true
	c.ClusterNodeAddr = "127.0.0.1:7000"
	c.ClusterSlots = map[string]string{
		"0-100":   "127.0.0.1:7000",
		"100-200": "127.0.0.1:7001",
	}
	if err := c.Validate(); err == nil {
		t.Fatal("expected overlapping cluster slots to fail")
	}
}

func TestClusterConfigRejectsOutOfRangeSlot(t *testing.T) {
	c := Default()
	c.ClusterEnabled = true
	c.ClusterNodeAddr = "127.0.0.1:7000"
	c.ClusterSlots = map[string]string{"16384": "127.0.0.1:7000"}
	if err := c.Validate(); err == nil {
		t.Fatal("expected out-of-range slot to fail")
	}
}

func TestClusterConfigRejectsFieldsWhenDisabled(t *testing.T) {
	c := Default()
	c.ClusterNodeAddr = "127.0.0.1:7000"
	if err := c.Validate(); err == nil {
		t.Fatal("expected cluster fields while disabled to fail")
	}
}


func TestClusterConfigRequiresControlAuth(t *testing.T) {
	c := Default()
	c.ClusterEnabled = true
	c.ClusterNodeAddr = "127.0.0.1:7000"
	c.ClusterSlots = map[string]string{"0-16383": "127.0.0.1:7000"}
	if err := c.Validate(); err == nil || err.Error() != "cluster_enabled requires cluster_control_auth" {
		t.Fatalf("err=%v", err)
	}
}

func TestClusterControlAuthEnv(t *testing.T) {
	c := Default()
	t.Setenv("SNUGKV_CLUSTER_CONTROL_AUTH", "env-cluster-secret")
	if err := c.ApplyEnv(); err != nil {
		t.Fatal(err)
	}
	if c.ClusterControlAuth != "env-cluster-secret" {
		t.Fatalf("cluster control auth=%q", c.ClusterControlAuth)
	}
}

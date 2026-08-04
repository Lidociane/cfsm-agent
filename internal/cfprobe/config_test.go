package cfprobe

import "testing"

func TestNormalizeInterfaceList(t *testing.T) {
	got, err := normalizeInterfaceList(" eth0,ens3,eth0, pppoe-wan ")
	if err != nil {
		t.Fatalf("normalizeInterfaceList returned error: %v", err)
	}
	if got != "eth0,ens3,pppoe-wan" {
		t.Fatalf("unexpected normalized interfaces: %q", got)
	}
}

func TestNormalizeInterfaceListRejectsBadName(t *testing.T) {
	if _, err := normalizeInterfaceList("eth0, bad/name"); err == nil {
		t.Fatal("expected invalid interface name to be rejected")
	}
}

package k8s

import "testing"

func TestValidName(t *testing.T) {
	cases := map[string]bool{
		"dev":           true,
		"dev-2":         true,
		"a1-b2-c3":      true,
		"":              false,
		"Dev":           false,
		"dev_test":      false,
		"dev.test":      false,
		"../etc/passwd": false,
		"dev/test":      false,
		" dev":          false,
	}
	for name, want := range cases {
		if got := ValidName(name); got != want {
			t.Errorf("ValidName(%q) = %v, want %v", name, got, want)
		}
	}
}

func TestCheckNameLength(t *testing.T) {
	if err := CheckNameLength("dev"); err != nil {
		t.Errorf("CheckNameLength(\"dev\") returned error: %v", err)
	}

	long := ""
	for len(prefix(long)+"control-plane") <= maxNodeNameLen {
		long += "x"
	}
	if err := CheckNameLength(long); err == nil {
		t.Errorf("CheckNameLength(%q) = nil, want error (name %q is %d chars)",
			long, ControlPlane(long), len(ControlPlane(long)))
	}
}

func TestControlPlaneAndWorker(t *testing.T) {
	if got, want := ControlPlane("dev"), "orchard-dev-control-plane"; got != want {
		t.Errorf("ControlPlane(\"dev\") = %q, want %q", got, want)
	}
	if got, want := Worker("dev", 3), "orchard-dev-worker-3"; got != want {
		t.Errorf("Worker(\"dev\", 3) = %q, want %q", got, want)
	}
}

func TestWorkerIndex(t *testing.T) {
	cases := []struct {
		cluster, name string
		wantIdx       int
		wantOK        bool
	}{
		{"dev", "orchard-dev-worker-3", 3, true},
		{"dev", "orchard-dev-worker-0", 0, true},
		{"dev", "orchard-dev-control-plane", 0, false},
		{"dev", "orchard-other-worker-1", 0, false},
		{"dev", "orchard-dev-worker-notanumber", 0, false},
	}
	for _, c := range cases {
		idx, ok := WorkerIndex(c.cluster, c.name)
		if idx != c.wantIdx || ok != c.wantOK {
			t.Errorf("WorkerIndex(%q, %q) = (%d, %v), want (%d, %v)",
				c.cluster, c.name, idx, ok, c.wantIdx, c.wantOK)
		}
	}
}

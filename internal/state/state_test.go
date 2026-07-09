package state

import (
	"os"
	"path/filepath"
	"testing"
)

func withTempHome(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	t.Setenv("HOME", dir)
	return dir
}

func TestSaveLoadRoundTrip(t *testing.T) {
	withTempHome(t)

	c := Cluster{
		Name: "dev", Image: "docker.io/kindest/node@sha256:abc", CPCPUs: "4",
		CPMemory: "4096M", WorkerCPUs: "4", WorkerMem: "2048M", CNI: "kindnet",
		NoMetrics: true, Headlamp: true,
	}
	if err := Save(c); err != nil {
		t.Fatalf("Save: %v", err)
	}

	got, err := Load("dev")
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if got != c {
		t.Errorf("Load() = %+v, want %+v", got, c)
	}
}

func TestLoadMissing(t *testing.T) {
	withTempHome(t)

	if _, err := Load("nope"); err == nil {
		t.Error("Load(\"nope\") = nil error, want an error for a missing state file")
	}
}

func TestDeleteMissingIsNil(t *testing.T) {
	withTempHome(t)

	if err := Delete("nope"); err != nil {
		t.Errorf("Delete on a missing record returned %v, want nil", err)
	}
}

func TestDeleteRemovesFile(t *testing.T) {
	home := withTempHome(t)

	if err := Save(Cluster{Name: "dev"}); err != nil {
		t.Fatalf("Save: %v", err)
	}
	p := filepath.Join(home, ".orchard", "clusters", "dev.json")
	if _, err := os.Stat(p); err != nil {
		t.Fatalf("expected state file at %s: %v", p, err)
	}
	if err := Delete("dev"); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, err := os.Stat(p); !os.IsNotExist(err) {
		t.Errorf("expected state file removed, stat returned: %v", err)
	}
}

func TestPathRejectsTraversal(t *testing.T) {
	withTempHome(t)

	for _, name := range []string{"../escape", "../../etc/passwd", "", "has/slash", "Uppercase"} {
		if err := Save(Cluster{Name: name}); err == nil {
			t.Errorf("Save with name %q succeeded, want an error rejecting the name", name)
		}
		if _, err := Load(name); err == nil {
			t.Errorf("Load(%q) succeeded, want an error rejecting the name", name)
		}
		if err := Delete(name); err == nil {
			t.Errorf("Delete(%q) succeeded, want an error rejecting the name", name)
		}
	}
}

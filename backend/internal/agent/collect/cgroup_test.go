package collect

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestPodUIDFromCgroupDir(t *testing.T) {
	cases := []struct {
		name    string
		wantUID string
		wantOK  bool
	}{
		{"pod1a2b3c4d-5e6f-7890-abcd-ef1234567890", "1a2b3c4d-5e6f-7890-abcd-ef1234567890", true},
		{"kubepods-burstable-pod1a2b3c4d_5e6f_7890_abcd_ef1234567890.slice", "1a2b3c4d-5e6f-7890-abcd-ef1234567890", true},
		{"kubepods-pod1a2b3c4d_5e6f_7890_abcd_ef1234567890.slice", "1a2b3c4d-5e6f-7890-abcd-ef1234567890", true},
		{"kubepods.slice", "", false},
		{"kubepods-burstable.slice", "", false},
		{"kubepods", "", false},
		{"besteffort", "", false},
		// A container's own sub-cgroup one level under its pod's - must not be mistaken for the pod
		// itself, even though it is nested right under a directory that does match.
		{"docker-abc123def456.scope", "", false},
	}
	for _, c := range cases {
		uid, ok := podUIDFromCgroupDir(c.name)
		if ok != c.wantOK || uid != c.wantUID {
			t.Errorf("podUIDFromCgroupDir(%q) = (%q, %v), want (%q, %v)", c.name, uid, ok, c.wantUID, c.wantOK)
		}
	}
}

// ino returns path's real inode number, the same number bpf_get_current_cgroup_id() would return for a
// task in that cgroup on a real cgroupfs - letting this test exercise cgroupIDToPodUID's actual matching
// logic against a real, if fake-rooted, directory tree rather than a mock.
func ino(t *testing.T, path string) uint64 {
	t.Helper()
	info, err := os.Stat(path)
	if err != nil {
		t.Fatal(err)
	}
	sys, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		t.Fatal("not a *syscall.Stat_t on this platform")
	}
	return sys.Ino
}

func TestCgroupIDToPodUIDMatchesByRealInode(t *testing.T) {
	root := t.TempDir()
	systemdDriver := "kubepods.slice/kubepods-burstable.slice/kubepods-burstable-pod1a2b3c4d_5e6f_7890_abcd_ef1234567890.slice"
	cgroupfsDriver := "kubepods/besteffort/podaaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee"
	for _, rel := range []string{systemdDriver, cgroupfsDriver} {
		if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
			t.Fatal(err)
		}
	}

	systemdID := ino(t, filepath.Join(root, systemdDriver))
	cgroupfsID := ino(t, filepath.Join(root, cgroupfsDriver))

	if uid, ok := cgroupIDToPodUID(root, systemdID); !ok || uid != "1a2b3c4d-5e6f-7890-abcd-ef1234567890" {
		t.Errorf("systemd-driver match = (%q, %v), want (1a2b3c4d-5e6f-7890-abcd-ef1234567890, true)", uid, ok)
	}
	if uid, ok := cgroupIDToPodUID(root, cgroupfsID); !ok || uid != "aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee" {
		t.Errorf("cgroupfs-driver match = (%q, %v), want (aaaaaaaa-bbbb-cccc-dddd-eeeeeeeeeeee, true)", uid, ok)
	}
}

func TestCgroupIDToPodUIDNoMatchOrZeroID(t *testing.T) {
	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "kubepods.slice/kubepods-burstable.slice"), 0o755); err != nil {
		t.Fatal(err)
	}
	if _, ok := cgroupIDToPodUID(root, 0); ok {
		t.Error("id=0 must never match, even if some real cgroup happened to have inode 0")
	}
	if _, ok := cgroupIDToPodUID(root, 999999999); ok {
		t.Error("an id with no matching pod directory in the tree must report no match")
	}
}

func TestWorkloadForCgroupResolvesThroughPodUIDs(t *testing.T) {
	root := t.TempDir()
	rel := "kubepods.slice/kubepods-besteffort.slice/kubepods-besteffort-pod1a2b3c4d_5e6f_7890_abcd_ef1234567890.slice"
	if err := os.MkdirAll(filepath.Join(root, rel), 0o755); err != nil {
		t.Fatal(err)
	}
	id := ino(t, filepath.Join(root, rel))

	old := cgroupRoot
	cgroupRoot = root
	defer func() { cgroupRoot = old }()

	ix := &Index{PodUIDs: map[string]string{"1a2b3c4d-5e6f-7890-abcd-ef1234567890": "shop/Deployment/cart"}}
	if key, ok := ix.WorkloadForCgroup(id); !ok || key != "shop/Deployment/cart" {
		t.Errorf("WorkloadForCgroup = (%q, %v), want (shop/Deployment/cart, true)", key, ok)
	}

	// id=0 (never captured) must short-circuit before ever touching the filesystem - no match, not an
	// accidental one against whatever cgroup happens to have inode 0 on this host.
	if _, ok := ix.WorkloadForCgroup(0); ok {
		t.Error("id=0 must never resolve")
	}

	// A UID the cgroup tree resolves to, but that this cluster's own Index never saw as a known pod
	// (PodUIDs has no entry for it) - ok must be false, not a zero-value key mistaken for a match.
	ix2 := &Index{PodUIDs: map[string]string{}}
	if _, ok := ix2.WorkloadForCgroup(id); ok {
		t.Error("a resolved UID with no corresponding PodUIDs entry must not resolve")
	}
}

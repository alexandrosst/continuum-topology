package collect

import (
	"io/fs"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"
)

// cgroupRoot is where the host's cgroup v2 hierarchy is mounted - overridable in tests the same way
// collector/saturation.go's sysClassNet is.
var cgroupRoot = "/sys/fs/cgroup"

// CgroupRootForTest points WorkloadForCgroup's filesystem walk at root instead of the real
// /sys/fs/cgroup, for a test (in this package or another, such as flow's own resolve.go tests) that
// needs to exercise cgroup id resolution against a fake, throwaway directory tree rather than the real
// host's cgroup hierarchy. Returns a restore function; the caller defers it.
func CgroupRootForTest(root string) (restore func()) {
	old := cgroupRoot
	cgroupRoot = root
	return func() { cgroupRoot = old }
}

// cgroupWalkMaxDepth bounds cgroupIDToPodUID's walk: a pod's own cgroup is never nested deeper than this
// under cgroupRoot in practice (root -> kubepods[.slice] -> a QoS class[.slice] -> the pod itself, three
// levels), so there is no need for - and this deliberately avoids - an unbounded walk of every cgroup on
// the host.
const cgroupWalkMaxDepth = 4

// podCgroupDirPattern matches a Kubernetes pod's own cgroup v2 directory, under either major cgroup
// driver's naming convention: the cgroupfs driver names it "pod<uid>" (uid in its normal dashed form,
// e.g. "pod1a2b3c4d-5e6f-7890-abcd-ef1234567890"), and the systemd driver names it
// "...-pod<uid_with_dashes_replaced_by_underscores>.slice" (e.g.
// "kubepods-burstable-pod1a2b3c4d_5e6f_7890_abcd_ef1234567890.slice"). Both end up matched by this one
// pattern: "pod" followed by 32 hex digits grouped like a UUID, with either dashes or underscores,
// optionally followed by ".slice".
var podCgroupDirPattern = regexp.MustCompile(`pod([0-9a-fA-F]{8}[_-][0-9a-fA-F]{4}[_-][0-9a-fA-F]{4}[_-][0-9a-fA-F]{4}[_-][0-9a-fA-F]{12})(?:\.slice)?$`)

// podUIDFromCgroupDir extracts a pod UID (in its normal dashed form) from a cgroup v2 directory name that
// names a specific pod's own cgroup - see podCgroupDirPattern's own doc comment for the two driver
// conventions it recognizes. ok is false for anything else: an intermediate "kubepods"/"kubepods.slice"/
// "kubepods-burstable.slice" level, a container's own sub-cgroup one level under its pod's, or a cgroup
// that has nothing to do with Kubernetes at all.
func podUIDFromCgroupDir(name string) (uid string, ok bool) {
	m := podCgroupDirPattern.FindStringSubmatch(name)
	if m == nil {
		return "", false
	}
	return strings.ToLower(strings.ReplaceAll(m[1], "_", "-")), true
}

// cgroupIDToPodUID walks root (normally cgroupRoot) looking for the one pod cgroup directory whose
// kernel cgroup id matches id, and returns the pod UID named in that directory's own name. The id a
// cgroup v2 directory carries is its own inode number on cgroupfs - the exact same number
// bpf_get_current_cgroup_id() returns from inside the kernel for a task in that cgroup (see flow.c's own
// sock_info.cgroup_id doc comment), so matching by stat'ing each candidate directory and comparing its
// inode is exact, not a guess.
//
// Best-effort and deliberately approximate: it recognizes the two cgroup drivers Kubernetes itself ships
// (cgroupfs and systemd) under their own conventional naming (podCgroupDirPattern), but a cluster running
// a third-party container runtime or cgroup layout, or cgroup v1 (no numeric cgroup ids at all, so
// bpf_get_current_cgroup_id itself would not even have fired), will simply never match - id just stays
// unresolved, exactly as if this function did not exist. No attempt is made to resolve a container-level
// sub-cgroup back to its pod, or to walk past cgroupWalkMaxDepth; only an exact pod-level cgroup directory
// match counts. This is the one piece of genuinely new, best-effort filesystem-walking logic Item 3 adds -
// see collect.Index.WorkloadForCgroup's own doc comment for why: no existing cgroup-path-to-pod resolution
// exists anywhere else in this codebase to build on (probe/read.go's own cgroup walk, for OOM-kill
// accounting, explicitly does not need to know which cgroup belongs to which pod at all).
func cgroupIDToPodUID(root string, id uint64) (uid string, ok bool) {
	if id == 0 {
		return "", false
	}
	var found string
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // an unreadable subtree (permissions, a cgroup that disappeared mid-walk) - skip it
		}
		if !d.IsDir() {
			return nil
		}
		if path == root {
			return nil
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil
		}
		if strings.Count(rel, string(filepath.Separator))+1 > cgroupWalkMaxDepth {
			return filepath.SkipDir
		}
		podUID, isPod := podUIDFromCgroupDir(d.Name())
		if !isPod {
			return nil
		}
		info, statErr := d.Info()
		if statErr != nil {
			return nil
		}
		sys, isSys := info.Sys().(*syscall.Stat_t)
		if !isSys {
			return nil
		}
		if sys.Ino == id {
			found = podUID
			return filepath.SkipAll
		}
		return nil
	})
	if found == "" {
		return "", false
	}
	return found, true
}

// WorkloadForCgroup is resolve.go's one, narrow way to disambiguate a ROLE_CLIENT row whose caller
// address could not be placed as a specific pod (pod(local) failed) but was recognized as this node's own
// address (ix.Nodes[local] is true) - the hostNetwork-pod case FlowEndpoint's own NODE doc comment
// describes: such a pod shares the node's address, so IP alone cannot tell its traffic apart from the
// node's own processes (kubelet, a static pod, ...).
//
// id is RawFlow.cgroup_id, read off the dialing process's own cgroup v2 id at SYN time (see flow.c's
// sock_info.cgroup_id and flow_val.cgroup_id doc comments) - 0 means no cgroup id was ever captured (a
// conntrack-derived report, or a kernel too old for bpf_get_current_cgroup_id), in which case this always
// returns ok=false immediately, no filesystem access at all.
//
// This is deliberately minimal: it walks the live cgroup filesystem once per call (cgroupIDToPodUID) and
// recognizes only Kubernetes' own two cgroup driver conventions - see that function's own doc comment for
// exactly what it does and does not cover. It is only ever called from the one narrow fallback path
// described above, not from every flow, so the cost of a shallow filesystem walk there is acceptable.
// Callers that get ok=false should fall back to attributing the row to the node itself (FlowEndpoint_NODE)
// rather than dropping it - this function stopping short of a match is an expected, common outcome (a
// bare node process, an unsupported cgroup driver, cgroup v1), not a failure to report.
func (ix *Index) WorkloadForCgroup(id uint64) (key string, ok bool) {
	if id == 0 || len(ix.PodUIDs) == 0 {
		return "", false
	}
	uid, found := cgroupIDToPodUID(cgroupRoot, id)
	if !found {
		return "", false
	}
	key, ok = ix.PodUIDs[uid]
	return key, ok
}

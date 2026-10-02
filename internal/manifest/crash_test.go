package manifest

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
	"testing"
)

// These tests kill a real process with SIGKILL at an exact point inside
// Replace, then open the database from a fresh process and check what
// survived. SIGKILL gives the process no chance to clean up or roll back,
// which is the same as a power cut as far as the process is concerned.
//
// Mechanism: the test re-runs its own binary (os.Args[0]) with an environment
// variable set. In that child, TestCrashHelper opens the database, installs a
// hook that kills the process at the named stage, and runs Replace.

const (
	envDB    = "STRATA_CRASH_DB"
	envStage = "STRATA_CRASH_STAGE"
)

func TestCrashHelper(t *testing.T) {
	path, stage := os.Getenv(envDB), os.Getenv(envStage)
	if path == "" {
		t.Skip("only runs as a child process of the crash tests")
	}
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m.testHook = func(s string) {
		if s == stage {
			syscall.Kill(os.Getpid(), syscall.SIGKILL)
			select {} // wait for the signal to land
		}
	}
	err = m.Replace(ctx, []Segment{seg("merged", 1, 20, 10)}, []string{"a", "b"})
	t.Fatalf("process survived stage %q (err=%v)", stage, err)
}

// crashAt seeds a manifest with active segments a and b, then runs Replace
// (merged <- a, b) in a child process that is killed at stage.
func crashAt(t *testing.T, stage string) *Manifest {
	t.Helper()
	path := filepath.Join(t.TempDir(), "manifest.db")
	m, err := Open(path)
	if err != nil {
		t.Fatal(err)
	}
	m.AddSegment(ctx, seg("a", 1, 10, 4))
	m.AddSegment(ctx, seg("b", 11, 20, 6))
	m.Close()

	cmd := exec.Command(os.Args[0], "-test.run=^TestCrashHelper$")
	cmd.Env = append(os.Environ(), envDB+"="+path, envStage+"="+stage)
	out, err := cmd.CombinedOutput()
	var exitErr *exec.ExitError
	if !errors.As(err, &exitErr) {
		t.Fatalf("child was not killed: err=%v output=%s", err, out)
	}
	ws := exitErr.Sys().(syscall.WaitStatus)
	if !ws.Signaled() || ws.Signal() != syscall.SIGKILL {
		t.Fatalf("child did not die from SIGKILL: %v\n%s", exitErr, out)
	}

	// Fresh open, as a restarted process would do.
	m2, err := Open(path)
	if err != nil {
		t.Fatalf("manifest would not reopen after crash: %v", err)
	}
	t.Cleanup(func() { m2.Close() })

	var check string
	if err := m2.db.QueryRowContext(ctx, "PRAGMA integrity_check").Scan(&check); err != nil || check != "ok" {
		t.Fatalf("integrity_check = %q (err %v)", check, err)
	}
	return m2
}

func TestCrashMidTransactionLeavesManifestUnchanged(t *testing.T) {
	// "inserted": new row written, nothing deleted yet.
	// "marked":   new row written AND old rows marked deleted, but not committed.
	// Both are the dangerous halves of the swap.
	for _, stage := range []string{"inserted", "marked"} {
		t.Run(stage, func(t *testing.T) {
			m := crashAt(t, stage)
			if got := fmt.Sprint(ids(mustList(t, m, StatusActive))); got != "[a b]" {
				t.Fatalf("active after crash = %s, want [a b] (a partial update leaked)", got)
			}
			if got := mustList(t, m, StatusDeleted); len(got) != 0 {
				t.Fatalf("deleted after crash = %v, want none", ids(got))
			}
		})
	}
}

// The control: proves the harness can tell a lost transaction from a durable
// one. Without it, the tests above would pass even if Replace never wrote anything.
func TestCrashAfterCommitIsDurable(t *testing.T) {
	m := crashAt(t, "committed")
	if got := fmt.Sprint(ids(mustList(t, m, StatusActive))); got != "[merged]" {
		t.Fatalf("active after crash = %s, want [merged]", got)
	}
	if got := fmt.Sprint(ids(mustList(t, m, StatusDeleted))); got != "[a b]" {
		t.Fatalf("deleted after crash = %s, want [a b]", got)
	}
}

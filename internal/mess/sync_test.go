package mess

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// twoUserSetup builds a hub plus two independent messes (alice, bob) that
// both track shared.txt, with bob cloned from alice's push.
func twoUserSetup(t *testing.T) (hub string, alice, bob *Store, aliceDir, bobDir string) {
	t.Helper()
	setIdent(t)
	base := t.TempDir()
	base, _ = filepath.EvalSymlinks(base)
	hub = filepath.Join(base, "hub.git")
	if err := HubInit(hub, testWriter(t)); err != nil {
		t.Fatal(err)
	}

	aliceDir = filepath.Join(base, "alice")
	os.MkdirAll(aliceDir, 0o755)
	if err := InitLocal(aliceDir, testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	alice = &Store{GitDir: filepath.Join(aliceDir, ".git-mess.git"), Root: aliceDir}
	write(t, aliceDir+"/shared.txt", "l1\nl2\nl3\nl4\nl5\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{Message: "initial"}, "shared.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	chdir(t, base)
	var err error
	bob, err = Clone(hub, filepath.Join(base, "bob"), testWriter(t))
	if err != nil {
		t.Fatal(err)
	}
	bobDir = bob.Root
	return hub, alice, bob, aliceDir, bobDir
}

func TestHubInitConfig(t *testing.T) {
	setIdent(t)
	base := t.TempDir()
	hub := filepath.Join(base, "hub.git")
	if err := HubInit(hub, testWriter(t)); err != nil {
		t.Fatal(err)
	}
	v, err := RunGit("", "--git-dir", hub, "config", "receive.denyNonFastForwards")
	if err != nil || v != "true" {
		t.Errorf("denyNonFastForwards = %q (%v), want true", v, err)
	}
	v, _ = RunGit("", "--git-dir", hub, "config", "receive.denyDeletes")
	if v != "false" {
		t.Errorf("denyDeletes = %q, want false", v)
	}
}

func TestCloneMaterializes(t *testing.T) {
	_, _, bob, _, bobDir := twoUserSetup(t)
	if read(t, bobDir+"/shared.txt") != "l1\nl2\nl3\nl4\nl5\n" {
		t.Error("clone did not materialize file content")
	}
	var buf bytes.Buffer
	if err := bob.Status(nil, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "(clean)") {
		t.Errorf("clone should be clean, got:\n%s", buf.String())
	}
}

func TestPullFastForward(t *testing.T) {
	hub, alice, bob, aliceDir, bobDir := twoUserSetup(t)

	write(t, aliceDir+"/shared.txt", "l1 EDIT\nl2\nl3\nl4\nl5\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{Message: "alice edit"}, "shared.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "fast-forward") {
		t.Errorf("want fast-forward, got:\n%s", buf.String())
	}
	if read(t, bobDir+"/shared.txt") != "l1 EDIT\nl2\nl3\nl4\nl5\n" {
		t.Error("fast-forward did not update bob's file")
	}
}

func TestPushRejectedOnDivergence(t *testing.T) {
	hub, alice, bob, aliceDir, bobDir := twoUserSetup(t)

	write(t, aliceDir+"/shared.txt", "l1 A\nl2\nl3\nl4\nl5\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{}, "shared.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	write(t, bobDir+"/shared.txt", "l1\nl2\nl3\nl4\nl5 B\n")
	chdir(t, bobDir)
	snap(t, bob, SnapshotOpts{}, "shared.txt")
	var errBuf bytes.Buffer
	if err := bob.Push(hub, "", testWriter(t), &errBuf); err == nil {
		t.Fatal("expected push rejection")
	}
	if !strings.Contains(errBuf.String(), "pull") {
		t.Errorf("rejection should advise pulling, got:\n%s", errBuf.String())
	}
}

func TestDivergedCleanMerge(t *testing.T) {
	hub, alice, bob, aliceDir, bobDir := twoUserSetup(t)

	write(t, aliceDir+"/shared.txt", "l1 A\nl2\nl3\nl4\nl5\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{Message: "alice"}, "shared.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	write(t, bobDir+"/shared.txt", "l1\nl2\nl3\nl4\nl5 B\n")
	chdir(t, bobDir)
	snap(t, bob, SnapshotOpts{Message: "bob"}, "shared.txt")

	var buf bytes.Buffer
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "(merged)") {
		t.Fatalf("want clean merge, got:\n%s", buf.String())
	}
	if read(t, bobDir+"/shared.txt") != "l1 A\nl2\nl3\nl4\nl5 B\n" {
		t.Errorf("merge lost an edit: %q", read(t, bobDir+"/shared.txt"))
	}
	// merge commit has both parents
	parents, _ := bob.Git("log", "-1", "--format=%P", "refs/mess/shared.txt")
	if len(strings.Fields(parents)) != 2 {
		t.Errorf("merge commit should have 2 parents, got %q", parents)
	}
	// and bob can now push it
	if err := bob.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
}

func TestDivergedConflictMarkers(t *testing.T) {
	hub, alice, bob, aliceDir, bobDir := twoUserSetup(t)

	write(t, aliceDir+"/shared.txt", "l1 ALICE\nl2\nl3\nl4\nl5\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{}, "shared.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	write(t, bobDir+"/shared.txt", "l1 BOB\nl2\nl3\nl4\nl5\n")
	chdir(t, bobDir)
	snap(t, bob, SnapshotOpts{}, "shared.txt")

	var buf bytes.Buffer
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "CONFLICT") {
		t.Fatalf("want conflict report, got:\n%s", buf.String())
	}
	content := read(t, bobDir+"/shared.txt")
	for _, marker := range []string{"<<<<<<<", "=======", ">>>>>>>", "l1 ALICE", "l1 BOB"} {
		if !strings.Contains(content, marker) {
			t.Errorf("conflict file missing %q:\n%s", marker, content)
		}
	}
}

func TestPullSkipsDirtyHistory(t *testing.T) {
	hub, alice, bob, aliceDir, bobDir := twoUserSetup(t)

	write(t, aliceDir+"/shared.txt", "l1 A\nl2\nl3\nl4\nl5\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{}, "shared.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	// bob edits but does NOT snapshot
	write(t, bobDir+"/shared.txt", "l1\nl2 uncommitted\nl3\nl4\nl5\n")
	before, _ := bob.RevParse("refs/mess/shared.txt")

	var buf bytes.Buffer
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "SKIPPED") {
		t.Fatalf("want skip, got:\n%s", buf.String())
	}
	after, _ := bob.RevParse("refs/mess/shared.txt")
	if before != after {
		t.Error("dirty history's ref must not move")
	}
	if read(t, bobDir+"/shared.txt") != "l1\nl2 uncommitted\nl3\nl4\nl5\n" {
		t.Error("dirty file must not be touched")
	}
}

func TestTombstonePropagation(t *testing.T) {
	hub, alice, bob, _, bobDir := twoUserSetup(t)

	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	if err := alice.Delete("shared.txt", true, testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	// hub's history ref is gone, tombstone present
	if out, _ := RunGit("", "--git-dir", hub, "for-each-ref", "refs/mess/"); out != "" {
		t.Errorf("hub still has history refs:\n%s", out)
	}
	if out, _ := RunGit("", "--git-dir", hub, "for-each-ref", "refs/mess-tombstones/"); out == "" {
		t.Error("hub missing tombstone")
	}

	var buf bytes.Buffer
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "deleted (remote tombstone") {
		t.Fatalf("want tombstone deletion, got:\n%s", buf.String())
	}
	if _, ok := bob.RevParse("refs/mess/shared.txt"); ok {
		t.Error("bob's history should be deleted")
	}
	if _, err := os.Stat(bobDir + "/shared.txt"); err != nil {
		t.Error("bob's working file must be left on disk")
	}
}

func TestNewestWinsRevival(t *testing.T) {
	hub, alice, bob, _, bobDir := twoUserSetup(t)

	// alice deletes at T1
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	if err := alice.Delete("shared.txt", true, testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	// bob snapshots at T2 > T1: his work postdates the deletion
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-03T00:00:00")
	write(t, bobDir+"/shared.txt", "bob kept working\n")
	chdir(t, bobDir)
	snap(t, bob, SnapshotOpts{}, "shared.txt")

	var buf bytes.Buffer
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "local is newer — keeping") {
		t.Fatalf("newer local work must survive a tombstone, got:\n%s", buf.String())
	}
	if _, ok := bob.RevParse("refs/mess/shared.txt"); !ok {
		t.Error("bob's history was wrongly deleted")
	}
}

func TestStaleTombstoneLosesToNewerRemote(t *testing.T) {
	hub, alice, bob, aliceDir, _ := twoUserSetup(t)

	// bob deletes at T1
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	if err := bob.Delete("shared.txt", true, testWriter(t)); err != nil {
		t.Fatal(err)
	}

	// alice snapshots at T2 > T1 and pushes
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-03T00:00:00")
	write(t, aliceDir+"/shared.txt", "alice continues\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{}, "shared.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "revived by newer remote version") {
		t.Fatalf("want revival, got:\n%s", buf.String())
	}
	if _, ok := bob.RevParse("refs/mess/shared.txt"); !ok {
		t.Error("history should be revived on bob's side")
	}
	if _, ok := bob.RevParse("refs/mess-tombstones/shared.txt"); ok {
		t.Error("stale tombstone should be dropped")
	}
}

func TestPushSingleHistory(t *testing.T) {
	hub, alice, _, aliceDir, _ := twoUserSetup(t)

	write(t, aliceDir+"/other.txt", "o\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{}, "other.txt")
	write(t, aliceDir+"/shared.txt", "l1 x\nl2\nl3\nl4\nl5\n")
	snap(t, alice, SnapshotOpts{}, "shared.txt")

	if err := alice.Push(hub, "other.txt", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	out, _ := RunGit("", "--git-dir", hub, "for-each-ref", "--format=%(refname)", "refs/mess/")
	if !strings.Contains(out, "refs/mess/other.txt") {
		t.Errorf("other.txt not pushed:\n%s", out)
	}
	// shared.txt's new version must NOT have been pushed
	hubShared, _ := RunGit("", "--git-dir", hub, "rev-parse", "refs/mess/shared.txt")
	localShared, _ := alice.Git("rev-parse", "refs/mess/shared.txt")
	if hubShared == localShared {
		t.Error("named push leaked another history")
	}
}

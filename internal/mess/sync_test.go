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
	deleteFully(t, alice, "shared.txt", true)
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
	deleteFully(t, alice, "shared.txt", true)
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
	deleteFully(t, bob, "shared.txt", true)

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

func TestArchivePropagates(t *testing.T) {
	hub, alice, bob, aliceDir, bobDir := twoUserSetup(t)

	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	chdir(t, aliceDir)
	if err := alice.Archive("shared.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	// hub: active gone, archive present
	out, _ := RunGit("", "--git-dir", hub, "for-each-ref", "--format=%(refname)", "refs/mess/")
	if strings.Contains(out, "refs/mess/shared.txt") {
		t.Errorf("active ref survived on hub:\n%s", out)
	}
	if _, err := RunGit("", "--git-dir", hub, "rev-parse", "refs/mess-archive/shared.txt"); err != nil {
		t.Error("archive ref missing on hub")
	}

	// bob fetch previews, then pull applies
	var buf bytes.Buffer
	if err := bob.Fetch(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "archived on remote (pull will archive locally)") {
		t.Fatalf("want archive preview, got:\n%s", buf.String())
	}
	buf.Reset()
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "archived (remote archive") {
		t.Fatalf("want archive application, got:\n%s", buf.String())
	}
	if _, ok := bob.RevParse("refs/mess/shared.txt"); ok {
		t.Error("bob's active ref should be gone")
	}
	if _, ok := bob.RevParse("refs/mess-archive/shared.txt"); !ok {
		t.Error("bob should hold the archive")
	}
	if _, err := os.Stat(bobDir + "/shared.txt"); err != nil {
		t.Error("bob's file must stay on disk")
	}
}

func TestSnapshotAfterArchiveWinsAcrossSync(t *testing.T) {
	hub, alice, bob, _, bobDir := twoUserSetup(t)

	// alice archives at T1 and pushes
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	if err := alice.Archive("shared.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	// bob keeps working at T2 > T1 (he hasn't pulled the archive)
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-03T00:00:00")
	write(t, bobDir+"/shared.txt", "l1 bob\nl2\nl3\nl4\nl5\n")
	chdir(t, bobDir)
	snap(t, bob, SnapshotOpts{}, "shared.txt")

	var buf bytes.Buffer
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "remote archived it, but local is newer") {
		t.Fatalf("newer local work must survive an archive, got:\n%s", buf.String())
	}
	if _, ok := bob.RevParse("refs/mess/shared.txt"); !ok {
		t.Error("bob's active history was wrongly archived")
	}

	// bob's push restores the hub: active back, stale archive retired
	if err := bob.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := RunGit("", "--git-dir", hub, "rev-parse", "refs/mess/shared.txt"); err != nil {
		t.Error("hub active ref not restored")
	}
	if _, err := RunGit("", "--git-dir", hub, "rev-parse", "refs/mess-archive/shared.txt"); err == nil {
		t.Error("stale archive ref should be retired from hub")
	}

	// and alice's pull unarchives her copy
	buf.Reset()
	if err := alice.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "unarchived (remote has newer activity)") {
		t.Fatalf("want reactivation on alice's side, got:\n%s", buf.String())
	}
	if _, ok := alice.RevParse("refs/mess/shared.txt"); !ok {
		t.Error("alice's history should be active again")
	}
}

func TestMovePropagatesWithoutResurrection(t *testing.T) {
	hub, alice, bob, aliceDir, bobDir := twoUserSetup(t)

	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	chdir(t, aliceDir)
	if err := alice.Move("shared.txt", "renamed.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	// hub: old name gone, new name present
	out, _ := RunGit("", "--git-dir", hub, "for-each-ref", "--format=%(refname)", "refs/mess/")
	if strings.Contains(out, "refs/mess/shared.txt") {
		t.Errorf("old name survived on hub:\n%s", out)
	}
	if !strings.Contains(out, "refs/mess/renamed.txt") {
		t.Errorf("new name missing on hub:\n%s", out)
	}

	// bob pulls: old history deleted, new one adopted with full chain
	var buf bytes.Buffer
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if _, ok := bob.RevParse("refs/mess/shared.txt"); ok {
		t.Error("old name resurrected on bob's side")
	}
	if _, ok := bob.RevParse("refs/mess/renamed.txt"); !ok {
		t.Fatal("renamed history not adopted")
	}
	chdir(t, bobDir) // name resolution is cwd-relative
	mustLogCount(t, bob, "renamed.txt", 2) // initial + move commit
	if read(t, bobDir+"/renamed.txt") != "l1\nl2\nl3\nl4\nl5\n" {
		t.Error("renamed file not materialized for bob")
	}

	// alice's next pull must NOT bring the old name back
	buf.Reset()
	if err := alice.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if _, ok := alice.RevParse("refs/mess/shared.txt"); ok {
		t.Errorf("old name resurrected on alice's side:\n%s", buf.String())
	}
}

func TestRemoteManagement(t *testing.T) {
	s, _ := newLocalMess(t)
	if err := s.Remote([]string{"add", "backup", "/tmp/somewhere.git"}, testWriter(t)); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := s.Remote(nil, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "backup") || !strings.Contains(buf.String(), "/tmp/somewhere.git") {
		t.Errorf("remote not listed:\n%s", buf.String())
	}
	if err := s.Remote([]string{"remove", "backup"}, testWriter(t)); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	s.Remote(nil, &buf)
	if strings.Contains(buf.String(), "backup") {
		t.Errorf("remote not removed:\n%s", buf.String())
	}
}

func TestDefaultRemoteOrigin(t *testing.T) {
	hub, alice, _, _, _ := twoUserSetup(t)

	// no origin configured: empty remote must error with guidance
	err := alice.Push("", "", testWriter(t), testWriter(t))
	if err == nil || !strings.Contains(err.Error(), "origin") {
		t.Fatalf("want origin guidance, got %v", err)
	}

	if err := alice.Remote([]string{"add", "origin", hub}, testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if err := alice.Push("", "", testWriter(t), testWriter(t)); err != nil {
		t.Fatalf("push with default origin: %v", err)
	}
	var buf bytes.Buffer
	if err := alice.Fetch("", "", &buf); err != nil {
		t.Fatalf("fetch with default origin: %v", err)
	}
	if err := alice.Pull("", "", &buf); err != nil {
		t.Fatalf("pull with default origin: %v", err)
	}
}

func TestCloneSetsOrigin(t *testing.T) {
	hub, alice, bob, aliceDir, _ := twoUserSetup(t)

	url, err := bob.Git("remote", "get-url", "origin")
	if err != nil || url != hub {
		t.Fatalf("clone should set origin to %s, got %q (%v)", hub, url, err)
	}
	// bob can sync with no remote argument at all
	write(t, aliceDir+"/shared.txt", "l1 A\nl2\nl3\nl4\nl5\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{}, "shared.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	if err := bob.Pull("", "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "fast-forward") {
		t.Errorf("default-origin pull failed:\n%s", buf.String())
	}
}

func TestFetchPreviewsWithoutMutating(t *testing.T) {
	hub, alice, bob, aliceDir, bobDir := twoUserSetup(t)

	// remote ahead: alice pushes an edit
	write(t, aliceDir+"/shared.txt", "l1 A\nl2\nl3\nl4\nl5\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{}, "shared.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	before, _ := bob.RevParse("refs/mess/shared.txt")
	var buf bytes.Buffer
	if err := bob.Fetch(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "shared.txt: remote ahead (pull will fast-forward)") {
		t.Fatalf("want fast-forward preview, got:\n%s", buf.String())
	}
	// nothing local moved
	after, _ := bob.RevParse("refs/mess/shared.txt")
	if before != after {
		t.Error("fetch must not move history refs")
	}
	if read(t, bobDir+"/shared.txt") != "l1\nl2\nl3\nl4\nl5\n" {
		t.Error("fetch must not touch files")
	}
	// fetched copy is kept locally
	if _, ok := bob.RevParse("refs/mess-fetched/shared.txt"); !ok {
		t.Error("fetched ref not retained")
	}

	// diverged: bob snapshots his own edit
	write(t, bobDir+"/shared.txt", "l1\nl2\nl3\nl4\nl5 B\n")
	chdir(t, bobDir)
	snap(t, bob, SnapshotOpts{}, "shared.txt")
	buf.Reset()
	if err := bob.Fetch(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "shared.txt: diverged (pull will merge)") {
		t.Errorf("want diverged preview, got:\n%s", buf.String())
	}
}

func TestFetchPreviewsRemoteDeletion(t *testing.T) {
	hub, alice, bob, _, _ := twoUserSetup(t)

	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	deleteFully(t, alice, "shared.txt", true)
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := bob.Fetch(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "shared.txt: deleted on remote (pull will delete locally)") {
		t.Fatalf("want deletion preview, got:\n%s", buf.String())
	}
	if _, ok := bob.RevParse("refs/mess/shared.txt"); !ok {
		t.Error("fetch must not apply the deletion")
	}
}

func TestListRemote(t *testing.T) {
	hub, alice, _, aliceDir, _ := twoUserSetup(t)

	write(t, aliceDir+"/second.txt", "s\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{}, "second.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := alice.List(hub, false, &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	for _, want := range []string{"shared.txt  [", "second.txt  ["} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %q in remote list:\n%s", want, out)
		}
	}
	// the listed sha must be the remote tip, abbreviated
	tip, _ := alice.Git("rev-parse", "refs/mess/shared.txt")
	if !strings.Contains(out, "["+tip[:7]+"]") {
		t.Errorf("want tip %s in:\n%s", tip[:7], out)
	}

	// delete one history: remote list must mark it deleted
	deleteFully(t, alice, "second.txt", true)
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	buf.Reset()
	if err := alice.List(hub, false, &buf); err != nil {
		t.Fatal(err)
	}
	out = buf.String()
	if !strings.Contains(out, "second.txt  (deleted)") {
		t.Errorf("want '(deleted)' marker:\n%s", out)
	}
	if strings.Contains(out, "second.txt  [") {
		t.Errorf("deleted history must not list a tip:\n%s", out)
	}
	if !strings.Contains(out, "shared.txt  [") {
		t.Errorf("live history lost from listing:\n%s", out)
	}
}

func TestListLocalUnaffectedByRemoteArg(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/only.txt", "x\n")
	snap(t, s, SnapshotOpts{}, "only.txt")

	var buf bytes.Buffer
	if err := s.List("", false, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "only.txt") {
		t.Errorf("local list broken:\n%s", buf.String())
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

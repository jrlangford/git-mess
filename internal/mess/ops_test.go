package mess

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSnapshotChainAndNoChange(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/notes.txt", "v1\n")
	out := snap(t, s, SnapshotOpts{Message: "first"}, "notes.txt")
	if !strings.Contains(out, "notes.txt -> ") {
		t.Fatalf("unexpected output: %s", out)
	}
	write(t, dir+"/notes.txt", "v2\n")
	snap(t, s, SnapshotOpts{Message: "second"}, "notes.txt")
	mustLogCount(t, s, "notes.txt", 2)

	// identical content must not create a version
	out = snap(t, s, SnapshotOpts{}, "notes.txt")
	if !strings.Contains(out, "no changes") {
		t.Fatalf("expected no-change detection, got: %s", out)
	}
	mustLogCount(t, s, "notes.txt", 2)
}

func TestSnapshotMultiFileNeedsName(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/a.txt", "a\n")
	write(t, dir+"/b.txt", "b\n")
	err := s.Snapshot([]string{"a.txt", "b.txt"}, SnapshotOpts{}, testWriter(t))
	if err == nil || !strings.Contains(err.Error(), "need -n") {
		t.Fatalf("want -n error, got %v", err)
	}
	snap(t, s, SnapshotOpts{Name: "pair"}, "a.txt", "b.txt")
	mustLogCount(t, s, "pair", 1)
}

func TestDoubleTrackingBlockedAndForced(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/a.txt", "a\n")
	snap(t, s, SnapshotOpts{}, "a.txt")

	err := s.Snapshot([]string{"a.txt"}, SnapshotOpts{Name: "bundle"}, testWriter(t))
	if err == nil || !strings.Contains(err.Error(), "already tracked by mess 'a.txt'") {
		t.Fatalf("want double-tracking error, got %v", err)
	}
	snap(t, s, SnapshotOpts{Name: "bundle", Force: true}, "a.txt")
	mustLogCount(t, s, "bundle", 1)
}

func TestExecutableBitPreserved(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/run.sh", "#!/bin/sh\n")
	os.Chmod(dir+"/run.sh", 0o755)
	snap(t, s, SnapshotOpts{}, "run.sh")

	os.Remove(dir + "/run.sh")
	if err := s.Restore("run.sh", "", false, testWriter(t)); err != nil {
		t.Fatal(err)
	}
	fi, err := os.Stat(dir + "/run.sh")
	if err != nil {
		t.Fatal(err)
	}
	if fi.Mode()&0o111 == 0 {
		t.Error("executable bit lost through snapshot+restore")
	}
}

func TestShowRevAndPartialPath(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/cfg/app.ini", "secret=1\n")
	write(t, dir+"/cfg/flags.ini", "debug=on\n")
	snap(t, s, SnapshotOpts{Name: "configs"}, "cfg/app.ini", "cfg/flags.ini")

	var buf bytes.Buffer
	if err := s.Show("configs", "app.ini", "", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "secret=1\n" {
		t.Errorf("partial path: got %q", buf.String())
	}

	err := s.Show("configs", "", "", &buf)
	if err == nil || !strings.Contains(err.Error(), "multi-file history") {
		t.Errorf("want multi-file error, got %v", err)
	}
	err = s.Show("configs", "nope.ini", "", &buf)
	if err == nil || !strings.Contains(err.Error(), "no unique match") {
		t.Errorf("want no-match error, got %v", err)
	}
}

func TestShowOldRevision(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/f.txt", "old\n")
	snap(t, s, SnapshotOpts{}, "f.txt")
	var buf bytes.Buffer
	if err := s.Log("f.txt", &buf); err != nil {
		t.Fatal(err)
	}
	rev := strings.Fields(buf.String())[0]

	write(t, dir+"/f.txt", "new\n")
	snap(t, s, SnapshotOpts{}, "f.txt")

	buf.Reset()
	if err := s.Show("f.txt", rev, "", &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "old\n" {
		t.Errorf("got %q", buf.String())
	}
}

func TestStatusStates(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/mod.txt", "x\n")
	write(t, dir+"/gone.txt", "y\n")
	snap(t, s, SnapshotOpts{}, "mod.txt")
	snap(t, s, SnapshotOpts{}, "gone.txt")

	var buf bytes.Buffer
	if err := s.Status(nil, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "(clean)") {
		t.Errorf("want clean lines, got:\n%s", buf.String())
	}

	write(t, dir+"/mod.txt", "changed\n")
	os.Remove(dir + "/gone.txt")
	buf.Reset()
	if err := s.Status(nil, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "modified") || !strings.Contains(buf.String(), "missing") {
		t.Errorf("want modified+missing, got:\n%s", buf.String())
	}
}

func TestDiffFirstVersionAgainstNothing(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/solo.txt", "only\n")
	snap(t, s, SnapshotOpts{}, "solo.txt")

	var buf bytes.Buffer
	if err := s.Diff(DiffOpts{Name: "solo.txt"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "new file") || !strings.Contains(buf.String(), "+only") {
		t.Errorf("single-version diff should show creation, got:\n%s", buf.String())
	}
}

func TestDiffDisk(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/d.txt", "a\n")
	snap(t, s, SnapshotOpts{}, "d.txt")

	var buf bytes.Buffer
	if err := s.Diff(DiffOpts{Name: "d.txt", Disk: true}, &buf); err != nil {
		t.Fatal(err)
	}
	if buf.String() != "" {
		t.Errorf("clean disk: want empty diff, got:\n%s", buf.String())
	}

	write(t, dir+"/d.txt", "a\nb\n")
	buf.Reset()
	if err := s.Diff(DiffOpts{Name: "d.txt", Disk: true}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "+b") {
		t.Errorf("want +b in disk diff, got:\n%s", buf.String())
	}
}

func TestDiffWholeMess(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/one.txt", "1\n")
	write(t, dir+"/two.txt", "2\n")
	snap(t, s, SnapshotOpts{}, "one.txt")
	snap(t, s, SnapshotOpts{}, "two.txt")
	write(t, dir+"/one.txt", "1+\n")

	var buf bytes.Buffer
	if err := s.Diff(DiffOpts{Disk: true}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "=== one.txt ===") {
		t.Errorf("want section for one.txt, got:\n%s", buf.String())
	}
	if strings.Contains(buf.String(), "=== two.txt ===") {
		t.Errorf("clean history two.txt must be silent, got:\n%s", buf.String())
	}
}

func TestRestoreOldVersion(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/r.txt", "v1\n")
	snap(t, s, SnapshotOpts{}, "r.txt")
	var buf bytes.Buffer
	s.Log("r.txt", &buf)
	rev := strings.Fields(buf.String())[0]

	write(t, dir+"/r.txt", "v2\n")
	snap(t, s, SnapshotOpts{}, "r.txt")

	if err := s.Restore("r.txt", rev, false, testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if read(t, dir+"/r.txt") != "v1\n" {
		t.Errorf("restore did not bring back v1")
	}
}

func TestMoveMovesFileAndRecordsVersion(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/before.txt", "content\n")
	snap(t, s, SnapshotOpts{Message: "v1"}, "before.txt")

	if err := s.Move("before.txt", "after.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(dir + "/before.txt"); !os.IsNotExist(err) {
		t.Error("old file still exists")
	}
	if read(t, dir+"/after.txt") != "content\n" {
		t.Error("file content lost in move")
	}
	mustLogCount(t, s, "after.txt", 2) // v1 + move commit

	var buf bytes.Buffer
	if err := s.Status([]string{"after.txt"}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "(clean)") {
		t.Errorf("moved history should be clean, got:\n%s", buf.String())
	}
}

func TestMoveLabelOnlyKeepsFiles(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/x.txt", "x\n")
	snap(t, s, SnapshotOpts{Name: "labelled"}, "x.txt")

	if err := s.Move("labelled", "relabelled", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if read(t, dir+"/x.txt") != "x\n" {
		t.Error("label-only move must not touch files")
	}
	mustLogCount(t, s, "relabelled", 1) // no move commit for label renames
}

func TestMoveLeavesTombstoneForOldName(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/orig.txt", "x\n")
	snap(t, s, SnapshotOpts{}, "orig.txt")

	if err := s.Move("orig.txt", "new.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.RevParse("refs/mess-tombstones/orig.txt"); !ok {
		t.Error("move must tombstone the old name")
	}
	if _, ok := s.RevParse("refs/mess-tombstones/new.txt"); ok {
		t.Error("new name must not be tombstoned")
	}
}

func TestMoveOntoDeletedNameRevives(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/dead.txt", "d\n")
	snap(t, s, SnapshotOpts{}, "dead.txt")
	deleteFully(t, s, "dead.txt", false)
	os.Remove(dir + "/dead.txt")

	write(t, dir+"/live.txt", "l\n")
	snap(t, s, SnapshotOpts{}, "live.txt")
	if err := s.Move("live.txt", "dead.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.RevParse("refs/mess-tombstones/dead.txt"); ok {
		t.Error("moving onto a deleted name must drop its tombstone")
	}
}

func TestArchiveLifecycle(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/f.txt", "content\n")
	snap(t, s, SnapshotOpts{}, "f.txt")

	// deleting an active history is refused
	err := s.Delete("f.txt", false, testWriter(t))
	if err == nil || !strings.Contains(err.Error(), "archive it first") {
		t.Fatalf("want gated-delete error, got %v", err)
	}

	if err := s.Archive("f.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if _, ok := s.RevParse("refs/mess/f.txt"); ok {
		t.Error("archived history still active")
	}
	if _, ok := s.RevParse("refs/mess-archive/f.txt"); !ok {
		t.Error("archive ref missing")
	}
	if read(t, dir+"/f.txt") != "content\n" {
		t.Error("archive must not touch files on disk")
	}

	var buf bytes.Buffer
	if err := s.List("", false, &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "f.txt") {
		t.Errorf("archived history in active listing:\n%s", buf.String())
	}
	buf.Reset()
	if err := s.List("", true, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "f.txt") {
		t.Errorf("archived history missing from --archived listing:\n%s", buf.String())
	}

	if err := s.Unarchive("f.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	mustLogCount(t, s, "f.txt", 2) // v1 + archive marker
	if _, ok := s.RevParse("refs/mess-archive/f.txt"); ok {
		t.Error("archive ref should be gone after unarchive")
	}
}

func TestSnapshotUnarchives(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/f.txt", "v1\n")
	snap(t, s, SnapshotOpts{}, "f.txt")
	if err := s.Archive("f.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}

	write(t, dir+"/f.txt", "v2\n")
	out := snap(t, s, SnapshotOpts{}, "f.txt")
	if !strings.Contains(out, "unarchiving") {
		t.Fatalf("want unarchive note, got: %s", out)
	}
	mustLogCount(t, s, "f.txt", 3) // v1 + marker + v2
	if _, ok := s.RevParse("refs/mess-archive/f.txt"); ok {
		t.Error("archive ref should be gone after snapshot")
	}
}

func TestUntrackedIgnoresArchived(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/known.txt", "k\n")
	snap(t, s, SnapshotOpts{}, "known.txt")
	if err := s.Archive("known.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := s.Untracked("", &buf); err != nil {
		t.Fatal(err)
	}
	if strings.Contains(buf.String(), "known.txt") {
		t.Errorf("archived file should not be untracked:\n%s", buf.String())
	}
}

func TestDeleteTombstoneAndRevive(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/tmp.txt", "x\n")
	snap(t, s, SnapshotOpts{}, "tmp.txt")

	deleteFully(t, s, "tmp.txt", true)
	if _, ok := s.RevParse("refs/mess/tmp.txt"); ok {
		t.Fatal("history ref still exists after delete")
	}
	if _, ok := s.RevParse("refs/mess-tombstones/tmp.txt"); !ok {
		t.Fatal("tombstone missing after delete")
	}

	var buf bytes.Buffer
	if err := s.Snapshot([]string{"tmp.txt"}, SnapshotOpts{}, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "reviving") {
		t.Errorf("want revival note, got: %s", buf.String())
	}
	if _, ok := s.RevParse("refs/mess-tombstones/tmp.txt"); ok {
		t.Error("tombstone should be removed on revival")
	}
}

func TestDeletePrunePurgesObjects(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/secret.txt", "hunter2\n")
	snap(t, s, SnapshotOpts{}, "secret.txt")
	blob, _ := s.Git("hash-object", dir+"/secret.txt")

	deleteFully(t, s, "secret.txt", true)
	if _, err := s.Git("cat-file", "-e", blob); err == nil {
		t.Error("blob still present in store after delete --prune")
	}
}

func TestSnapshotAll(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/a.txt", "a\n")
	write(t, dir+"/b.txt", "b\n")
	snap(t, s, SnapshotOpts{}, "a.txt")
	snap(t, s, SnapshotOpts{}, "b.txt")
	write(t, dir+"/a.txt", "a2\n")

	var buf bytes.Buffer
	if err := s.SnapshotAll("bulk", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "a.txt -> ") {
		t.Errorf("dirty history not snapshotted:\n%s", buf.String())
	}
	if !strings.Contains(buf.String(), "b.txt  (clean)") {
		t.Errorf("clean history not reported clean:\n%s", buf.String())
	}
	mustLogCount(t, s, "a.txt", 2)
	mustLogCount(t, s, "b.txt", 1)
}

func TestSnapshotAllSkipsAllMissing(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/gone.txt", "x\n")
	snap(t, s, SnapshotOpts{}, "gone.txt")
	os.Remove(dir + "/gone.txt")

	var buf bytes.Buffer
	if err := s.SnapshotAll("", &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), "skipped") {
		t.Errorf("all-missing history must be skipped, got:\n%s", buf.String())
	}
	mustLogCount(t, s, "gone.txt", 1)
}

func TestUntracked(t *testing.T) {
	s, dir := newLocalMess(t)
	write(t, dir+"/tracked.txt", "t\n")
	write(t, dir+"/stray.txt", "s\n")
	write(t, dir+"/deep/nested.txt", "n\n")
	snap(t, s, SnapshotOpts{}, "tracked.txt")

	// nested mess must be skipped entirely
	if err := InitLocal(dir+"/sub", testWriter(t), testWriter(t)); err != nil {
		if !strings.Contains(err.Error(), "no such directory") {
			t.Fatal(err)
		}
		os.MkdirAll(dir+"/sub", 0o755)
		if err := InitLocal(dir+"/sub", testWriter(t), testWriter(t)); err != nil {
			t.Fatal(err)
		}
	}
	write(t, dir+"/sub/theirs.txt", "x\n")

	var buf bytes.Buffer
	if err := s.Untracked("", &buf); err != nil {
		t.Fatal(err)
	}
	out := buf.String()
	if strings.Contains(out, "tracked.txt") {
		t.Errorf("tracked file listed as untracked:\n%s", out)
	}
	for _, want := range []string{"stray.txt", "nested.txt"} {
		if !strings.Contains(out, want) {
			t.Errorf("missing %s in:\n%s", want, out)
		}
	}
	if strings.Contains(out, "theirs.txt") {
		t.Errorf("nested mess not pruned:\n%s", out)
	}
}

func TestStoreResolutionPrecedence(t *testing.T) {
	setIdent(t)
	dir := t.TempDir()
	dir, _ = filepath.EvalSymlinks(dir)
	sub := filepath.Join(dir, "a", "b")
	os.MkdirAll(sub, 0o755)

	// no local store anywhere -> global
	if got := ResolveStorePath(sub); got != GlobalStorePath() {
		t.Errorf("want global fallback, got %s", got)
	}
	// nearest .git-mess.git wins
	InitLocal(dir, testWriter(t), testWriter(t))
	if got := ResolveStorePath(sub); got != filepath.Join(dir, ".git-mess.git") {
		t.Errorf("want discovered store, got %s", got)
	}
	// env var beats discovery
	t.Setenv("GIT_MESS_STORE", "/tmp/elsewhere.git")
	if got := ResolveStorePath(sub); got != "/tmp/elsewhere.git" {
		t.Errorf("want env override, got %s", got)
	}
}

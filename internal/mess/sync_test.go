package mess

import (
	"bytes"
	"errors"
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

func TestPullConflictReturnsErrorAfterRecordingMerge(t *testing.T) {
	hub, alice, bob, aliceDir, bobDir := twoUserSetup(t)

	write(t, aliceDir+"/shared.txt", "remote edit\nl2\nl3\nl4\nl5\n")
	chdir(t, aliceDir)
	snap(t, alice, SnapshotOpts{}, "shared.txt")
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	write(t, bobDir+"/shared.txt", "local edit\nl2\nl3\nl4\nl5\n")
	chdir(t, bobDir)
	snap(t, bob, SnapshotOpts{}, "shared.txt")

	before, _ := bob.RevParse("refs/mess/shared.txt")
	var buf bytes.Buffer
	err := bob.Pull(hub, "shared.txt", &buf)
	if !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("Pull error = %v, want ErrMergeConflict", err)
	}
	after, _ := bob.RevParse("refs/mess/shared.txt")
	if after == before {
		t.Fatal("conflicted pull did not record a merge commit")
	}
	parents, _ := bob.Git("log", "-1", "--format=%P", after)
	if len(strings.Fields(parents)) != 2 {
		t.Fatalf("conflicted merge parents = %q, want two parents", parents)
	}
	content := read(t, bobDir+"/shared.txt")
	if !strings.Contains(content, "<<<<<<<") {
		t.Fatalf("conflict markers were not restored:\n%s", content)
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
	if err := bob.Pull(hub, "", &buf); !errors.Is(err, ErrMergeConflict) {
		t.Fatalf("Pull error = %v, want ErrMergeConflict", err)
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

func TestRemoteTombstonePreservesDirtyDiskRecovery(t *testing.T) {
	hub, alice, bob, _, bobDir := twoUserSetup(t)

	write(t, bobDir+"/shared.txt", "unsnapshotted local bytes\n")
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	deleteFully(t, alice, "shared.txt", true)
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	var buf bytes.Buffer
	if err := bob.Pull(hub, "", &buf); err != nil {
		t.Fatal(err)
	}
	refs := bob.ForEachRef("refs/mess-recovery")
	if len(refs) != 1 {
		t.Fatalf("want one recovery ref, got %v", refs)
	}
	content, err := bob.GitRaw("show", refs[0]+":shared.txt")
	if err != nil {
		t.Fatal(err)
	}
	if string(content) != "unsnapshotted local bytes\n" {
		t.Fatalf("recovery has %q", content)
	}
	if _, ok := bob.RevParse(refs[0] + "~1"); !ok {
		t.Fatal("recovery commit must retain the local history tip as its parent")
	}
	if !strings.Contains(buf.String(), "saved dirty disk state as recovery shared.txt/") {
		t.Fatalf("pull did not report recovery identifier:\n%s", buf.String())
	}

	buf.Reset()
	if err := bob.List("", false, true, &buf); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(buf.String(), strings.TrimPrefix(refs[0], "refs/mess-recovery/")) {
		t.Fatalf("recovery is not discoverable:\n%s", buf.String())
	}
	if err := bob.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if remoteRecoveries, err := RunGit("", "--git-dir", hub, "for-each-ref", "refs/mess-recovery/"); err != nil || remoteRecoveries != "" {
		t.Fatalf("local recovery leaked to remote: %q (%v)", remoteRecoveries, err)
	}
	id := strings.TrimPrefix(refs[0], "refs/mess-recovery/")
	write(t, bobDir+"/shared.txt", "overwritten later\n")
	if err := bob.Restore(id, "", false, testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if got := read(t, bobDir+"/shared.txt"); got != "unsnapshotted local bytes\n" {
		t.Fatalf("restored recovery content = %q", got)
	}
	if err := bob.Delete(id, false, testWriter(t)); err == nil || !strings.Contains(err.Error(), "retained until explicit prune") {
		t.Fatalf("recovery deletion without prune = %v", err)
	}
	if _, ok := bob.RevParse(refs[0]); !ok {
		t.Fatal("recovery disappeared without explicit prune")
	}
	if err := bob.Delete(id, true, testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if _, ok := bob.RevParse(refs[0]); ok {
		t.Fatal("recovery ref remains after explicit prune")
	}
}

func TestRemoteTombstoneDoesNotCreateCleanRecovery(t *testing.T) {
	hub, alice, bob, _, _ := twoUserSetup(t)

	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	deleteFully(t, alice, "shared.txt", true)
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if err := bob.Pull(hub, "", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if refs := bob.ForEachRef("refs/mess-recovery"); len(refs) != 0 {
		t.Fatalf("clean history created redundant recovery refs: %v", refs)
	}
}

func TestRemoteTombstoneRecoveryMirrorsPartialAndAllMissingDisk(t *testing.T) {
	for _, tc := range []struct {
		name      string
		removeAll bool
		wantPaths []string
		wantApp   string
	}{
		{name: "partial", wantPaths: []string{"config/app.toml", "config/worker.toml"}, wantApp: "A1\n"},
		{name: "all-missing", removeAll: true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			hub, alice, bob, aliceDir, bobDir := twoUserSetup(t)
			for _, file := range []struct{ path, body string }{
				{"config/app.toml", "A0\n"},
				{"config/cache.toml", "B0\n"},
				{"config/worker.toml", "C0\n"},
			} {
				write(t, filepath.Join(aliceDir, file.path), file.body)
			}
			chdir(t, aliceDir)
			snap(t, alice, SnapshotOpts{Name: "app-config"},
				"config/app.toml", "config/cache.toml", "config/worker.toml")
			if err := alice.Push(hub, "app-config", testWriter(t), testWriter(t)); err != nil {
				t.Fatal(err)
			}
			if err := bob.Pull(hub, "app-config", testWriter(t)); err != nil {
				t.Fatal(err)
			}

			if tc.removeAll {
				for _, p := range []string{"config/app.toml", "config/cache.toml", "config/worker.toml"} {
					if err := os.Remove(filepath.Join(bobDir, p)); err != nil {
						t.Fatal(err)
					}
				}
			} else {
				write(t, bobDir+"/config/app.toml", "A1\n")
				if err := os.Remove(bobDir + "/config/cache.toml"); err != nil {
					t.Fatal(err)
				}
			}

			t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
			deleteFully(t, alice, "app-config", true)
			if err := alice.Push(hub, "app-config", testWriter(t), testWriter(t)); err != nil {
				t.Fatal(err)
			}
			if err := bob.Pull(hub, "app-config", testWriter(t)); err != nil {
				t.Fatal(err)
			}

			refs := bob.ForEachRef("refs/mess-recovery")
			if len(refs) != 1 {
				t.Fatalf("want one recovery ref, got %v", refs)
			}
			paths, err := bob.TreePaths(refs[0])
			if err != nil {
				t.Fatal(err)
			}
			if strings.Join(paths, "\n") != strings.Join(tc.wantPaths, "\n") {
				t.Fatalf("recovery paths = %v, want %v", paths, tc.wantPaths)
			}
			if tc.wantApp != "" {
				content, err := bob.GitRaw("show", refs[0]+":config/app.toml")
				if err != nil || string(content) != tc.wantApp {
					t.Fatalf("recovery app content = %q (%v)", content, err)
				}
			}

			// Recovery restore reproduces omissions too, rather than merely
			// writing the files that remain in the recovery tree.
			for _, p := range []string{"config/app.toml", "config/cache.toml", "config/worker.toml"} {
				write(t, filepath.Join(bobDir, p), "later\n")
			}
			id := strings.TrimPrefix(refs[0], "refs/mess-recovery/")
			if err := bob.Restore(id, "", false, testWriter(t)); err != nil {
				t.Fatal(err)
			}
			for _, p := range []string{"config/app.toml", "config/cache.toml", "config/worker.toml"} {
				wantPresent := map[string]bool{
					"config/app.toml":    !tc.removeAll,
					"config/worker.toml": !tc.removeAll,
				}[p]
				_, err := os.Stat(filepath.Join(bobDir, p))
				if wantPresent && err != nil {
					t.Errorf("restore omitted %s: %v", p, err)
				}
				if !wantPresent && !os.IsNotExist(err) {
					t.Errorf("restore should remove %s, stat err = %v", p, err)
				}
			}
		})
	}
}

func TestRecoveryInstallFailureAbortsRemoteTombstone(t *testing.T) {
	hub, alice, bob, _, bobDir := twoUserSetup(t)
	write(t, bobDir+"/shared.txt", "dirty\n")
	before, _ := bob.RevParse("refs/mess/shared.txt")

	// Block creation of refs/mess-recovery/shared.txt/<tomb>.
	blocker := filepath.Join(bob.GitDir, "refs/mess-recovery/shared.txt")
	if err := os.MkdirAll(filepath.Dir(blocker), 0o755); err != nil {
		t.Fatal(err)
	}
	write(t, blocker, before+"\n")

	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	deleteFully(t, alice, "shared.txt", true)
	if err := alice.Push(hub, "", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}
	err := bob.Pull(hub, "", testWriter(t))
	if err == nil || !strings.Contains(err.Error(), "remote tombstone not applied") {
		t.Fatalf("want fail-closed recovery error, got %v", err)
	}
	after, ok := bob.RevParse("refs/mess/shared.txt")
	if !ok || after != before {
		t.Fatalf("active ref changed after recovery failure: before %s after %s", before, after)
	}
	if _, ok := bob.RevParse("refs/mess-tombstones/shared.txt"); ok {
		t.Fatal("remote tombstone was applied after recovery failure")
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
	chdir(t, bobDir)                       // name resolution is cwd-relative
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

func TestTargetedPushPublishesBothHalvesOfMove(t *testing.T) {
	hub, alice, bob, aliceDir, _ := twoUserSetup(t)

	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	chdir(t, aliceDir)
	if err := alice.Move("shared.txt", "renamed.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if err := alice.Push(hub, "renamed.txt", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	if _, err := RunGit("", "--git-dir", hub, "rev-parse", "refs/mess/renamed.txt"); err != nil {
		t.Fatal("hub is missing the new history")
	}
	if _, err := RunGit("", "--git-dir", hub, "rev-parse", "refs/mess/shared.txt"); err == nil {
		t.Fatal("hub retained the old history after targeted move push")
	}
	if _, err := RunGit("", "--git-dir", hub, "rev-parse", "refs/mess-tombstones/shared.txt"); err != nil {
		t.Fatal("hub is missing the old-name tombstone")
	}

	if err := bob.Pull(hub, "", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if _, ok := bob.RevParse("refs/mess/shared.txt"); ok {
		t.Fatal("peer retained the old history")
	}
	if _, ok := bob.RevParse("refs/mess/renamed.txt"); !ok {
		t.Fatal("peer did not adopt the renamed history")
	}
}

func TestTargetedPushPublishesUnpushedMoveChain(t *testing.T) {
	hub, alice, _, aliceDir, _ := twoUserSetup(t)

	t.Setenv("GIT_COMMITTER_DATE", "2030-01-02T00:00:00")
	chdir(t, aliceDir)
	if err := alice.Move("shared.txt", "intermediate.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	t.Setenv("GIT_COMMITTER_DATE", "2030-01-03T00:00:00")
	if err := alice.Move("intermediate.txt", "final.txt", testWriter(t)); err != nil {
		t.Fatal(err)
	}
	if err := alice.Push(hub, "final.txt", testWriter(t), testWriter(t)); err != nil {
		t.Fatal(err)
	}

	for _, oldName := range []string{"shared.txt", "intermediate.txt"} {
		if _, err := RunGit("", "--git-dir", hub, "rev-parse", "refs/mess/"+oldName); err == nil {
			t.Errorf("hub retained old history %s", oldName)
		}
		if _, err := RunGit("", "--git-dir", hub, "rev-parse", "refs/mess-tombstones/"+oldName); err != nil {
			t.Errorf("hub is missing tombstone for %s", oldName)
		}
	}
	if _, err := RunGit("", "--git-dir", hub, "rev-parse", "refs/mess/final.txt"); err != nil {
		t.Fatal("hub is missing final history")
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
	if err := alice.List(hub, false, false, &buf); err != nil {
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
	if err := alice.List(hub, false, false, &buf); err != nil {
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
	if err := s.List("", false, false, &buf); err != nil {
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

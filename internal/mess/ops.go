package mess

import (
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type indexEntry struct {
	Mode, Blob, Path string
}

func fileModeOf(path string) (string, error) {
	fi, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if fi.Mode()&0o111 != 0 {
		return "100755", nil
	}
	return "100644", nil
}

// buildTree writes entries through a throwaway index and returns the tree.
func (s *Store) buildTree(entries []indexEntry) (string, error) {
	dir, err := os.MkdirTemp("", "git-mess-idx")
	if err != nil {
		return "", err
	}
	defer os.RemoveAll(dir)
	env := []string{"GIT_INDEX_FILE=" + filepath.Join(dir, "index")}
	for _, e := range entries {
		if _, err := s.GitEnv(env, "update-index", "--add", "--cacheinfo",
			e.Mode+","+e.Blob+","+e.Path); err != nil {
			return "", err
		}
	}
	return s.GitEnv(env, "write-tree")
}

// DiskTree builds a throwaway tree from the current disk contents of rev's
// files. Missing files are omitted (they show as deletions when diffed).
func (s *Store) DiskTree(rev string) (string, error) {
	paths, err := s.TreePaths(rev)
	if err != nil {
		return "", err
	}
	var entries []indexEntry
	for _, p := range paths {
		dest := s.DiskPath(p)
		fi, err := os.Stat(dest)
		if err != nil || !fi.Mode().IsRegular() {
			continue
		}
		blob, err := s.Git("hash-object", "-w", dest)
		if err != nil {
			return "", err
		}
		mode := "100644"
		if fi.Mode()&0o111 != 0 {
			mode = "100755"
		}
		entries = append(entries, indexEntry{mode, blob, p})
	}
	return s.buildTree(entries)
}

// IsDirty reports whether the disk differs from a ref's tip.
func (s *Store) IsDirty(ref string) bool {
	dt, err := s.DiskTree(ref)
	if err != nil {
		return true
	}
	tip, err := s.Git("rev-parse", ref+"^{tree}")
	return err != nil || dt != tip
}

// trackedElsewhere reports which other history (if any) records treePath.
func (s *Store) trackedElsewhere(selfRef, treePath string) (string, bool) {
	for _, ref := range s.ForEachRef("refs/mess") {
		if ref == selfRef {
			continue
		}
		paths, err := s.TreePaths(ref)
		if err != nil {
			continue
		}
		for _, p := range paths {
			if p == treePath {
				return ShortName(ref), true
			}
		}
	}
	return "", false
}

// SnapshotOpts modify Snapshot behavior.
type SnapshotOpts struct {
	Name    string
	Message string
	Force   bool
}

// Snapshot records a new version of the given files.
func (s *Store) Snapshot(files []string, o SnapshotOpts, out io.Writer) error {
	name := o.Name
	if name == "" {
		if len(files) != 1 {
			return fmt.Errorf("git-mess: multi-file snapshots need -n <name>")
		}
		name = files[0]
	}
	if err := s.Ensure(); err != nil {
		return err
	}
	ref, err := s.NameToRef(name)
	if err != nil {
		return err
	}
	short := ShortName(ref)
	if _, ok := s.RevParse(ref); !ok {
		if _, tOk := s.RevParse("refs/mess-tombstones/" + short); tOk {
			s.Git("update-ref", "-d", "refs/mess-tombstones/"+short)
			fmt.Fprintf(out, "note: reviving previously deleted history %s\n", short)
		}
	}

	var entries []indexEntry
	for _, f := range files {
		fi, err := os.Stat(f)
		if err != nil || !fi.Mode().IsRegular() {
			return fmt.Errorf("git-mess: not a file: %s", f)
		}
		abs, err := filepath.Abs(f)
		if err != nil {
			return err
		}
		tp := s.TreePath(abs)
		if !o.Force {
			if other, found := s.trackedElsewhere(ref, tp); found {
				return fmt.Errorf(
					"git-mess: %s is already tracked by mess '%s'; add -f to force double tracking in this mess",
					f, other)
			}
		}
		blob, err := s.Git("hash-object", "-w", abs)
		if err != nil {
			return err
		}
		mode, err := fileModeOf(abs)
		if err != nil {
			return err
		}
		entries = append(entries, indexEntry{mode, blob, tp})
	}
	tree, err := s.buildTree(entries)
	if err != nil {
		return err
	}

	commitArgs := []string{"commit-tree", tree, "-m", o.Message}
	if o.Message == "" {
		commitArgs[3] = "snapshot: " + name
	}
	if parent, ok := s.RevParse(ref); ok {
		ptree, err := s.Git("rev-parse", parent+"^{tree}")
		if err != nil {
			return err
		}
		if ptree == tree {
			fmt.Fprintf(out, "git-mess: no changes since last snapshot of %s\n", name)
			return nil
		}
		commitArgs = append(commitArgs, "-p", parent)
	}
	commit, err := s.Git(commitArgs...)
	if err != nil {
		return err
	}
	if _, err := s.Git("update-ref", ref, commit); err != nil {
		return err
	}
	fmt.Fprintf(out, "%s -> %s\n", name, s.Short(commit))
	return nil
}

// SnapshotAll re-snapshots every history whose files changed on disk.
func (s *Store) SnapshotAll(msg string, out io.Writer) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	empty, err := s.EmptyTree()
	if err != nil {
		return err
	}
	for _, ref := range s.ForEachRef("refs/mess") {
		name := ShortName(ref)
		tip, _ := s.RevParse(ref)
		tree, err := s.DiskTree(ref)
		if err != nil {
			return err
		}
		tipTree, err := s.Git("rev-parse", ref+"^{tree}")
		if err != nil {
			return err
		}
		switch {
		case tree == tipTree:
			fmt.Fprintf(out, "%s  (clean)\n", name)
		case tree == empty:
			fmt.Fprintf(out, "%s  (all files missing — skipped; use 'git mess delete' if intended)\n", name)
		default:
			m := msg
			if m == "" {
				m = "snapshot: " + name
			}
			commit, err := s.Git("commit-tree", tree, "-m", m, "-p", tip)
			if err != nil {
				return err
			}
			if _, err := s.Git("update-ref", ref, commit); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s -> %s\n", name, s.Short(commit))
		}
	}
	return nil
}

// List prints every history with tip and age.
func (s *Store) List(out io.Writer) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	res, err := s.Git("for-each-ref", "refs/mess/",
		"--format=%(refname:lstrip=2)  [%(objectname:short)]  %(creatordate:relative)")
	if err != nil {
		return err
	}
	if res != "" {
		fmt.Fprintln(out, res)
	}
	return nil
}

// Log prints a history's versions.
func (s *Store) Log(name string, out io.Writer) error {
	ref, err := s.ResolveRef(name)
	if err != nil {
		return err
	}
	res, err := s.Git("log", "--format=%h  %ad  %an  %s",
		"--date=format:%Y-%m-%d %H:%M:%S", ref)
	if err != nil {
		return err
	}
	fmt.Fprintln(out, res)
	return nil
}

// statusChanges lists "modified"/"missing" lines for one history, empty if clean.
func (s *Store) statusChanges(ref string) ([]string, error) {
	entries, err := s.TreeEntries(ref)
	if err != nil {
		return nil, err
	}
	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	var changes []string
	for _, p := range paths {
		dest := s.DiskPath(p)
		fi, err := os.Stat(dest)
		if err != nil || !fi.Mode().IsRegular() {
			changes = append(changes, "  missing   "+dest)
			continue
		}
		h, err := s.Git("hash-object", dest)
		if err != nil {
			return nil, err
		}
		if h != entries[p].SHA {
			changes = append(changes, "  modified  "+dest)
		}
	}
	return changes, nil
}

// Status reports disk vs latest snapshot for the named histories (all if none).
func (s *Store) Status(names []string, out io.Writer) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	var refs []string
	if len(names) > 0 {
		for _, n := range names {
			ref, err := s.ResolveRef(n)
			if err != nil {
				return err
			}
			refs = append(refs, ref)
		}
	} else {
		refs = s.ForEachRef("refs/mess")
	}
	if len(refs) == 0 {
		fmt.Fprintln(out, "git-mess: no histories")
		return nil
	}
	for _, ref := range refs {
		changes, err := s.statusChanges(ref)
		if err != nil {
			return err
		}
		if len(changes) == 0 {
			fmt.Fprintf(out, "%s  (clean)\n", ShortName(ref))
		} else {
			fmt.Fprintln(out, ShortName(ref))
			for _, c := range changes {
				fmt.Fprintln(out, c)
			}
		}
	}
	return nil
}

// Show prints one file's content at a version. rev and path are optional;
// a non-revision second argument is treated as the path, and partial paths
// resolve when unique.
func (s *Store) Show(name, rev, path string, out io.Writer) error {
	ref, err := s.ResolveRef(name)
	if err != nil {
		return err
	}
	if rev != "" && path == "" {
		if _, ok := s.RevParse(rev + "^{commit}"); !ok {
			path, rev = rev, ""
		}
	}
	if rev == "" {
		rev = ref
	}
	paths, err := s.TreePaths(rev)
	if err != nil {
		return err
	}
	switch {
	case path == "":
		if len(paths) != 1 {
			return fmt.Errorf("git-mess: multi-file history, pass a path:\n%s", strings.Join(paths, "\n"))
		}
		path = paths[0]
	default:
		if !contains(paths, path) {
			var matches []string
			for _, p := range paths {
				if strings.Contains(p, path) {
					matches = append(matches, p)
				}
			}
			if len(matches) != 1 {
				return fmt.Errorf("git-mess: no unique match for '%s' in %s:\n%s",
					path, name, strings.Join(paths, "\n"))
			}
			path = matches[0]
		}
	}
	content, err := s.GitRaw("show", rev+":"+path)
	if err != nil {
		return err
	}
	_, err = out.Write(content)
	return err
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// DiffOpts modify Diff behavior.
type DiffOpts struct {
	Name string // empty = whole mess
	Rev1 string
	Rev2 string
	Disk bool
}

// Diff prints version diffs: last change by default, --disk against the
// filesystem, and the whole mess when no name is given.
func (s *Store) Diff(o DiffOpts, out io.Writer) error {
	if o.Name == "" {
		if err := s.Ensure(); err != nil {
			return err
		}
		for _, ref := range s.ForEachRef("refs/mess") {
			var d string
			var err error
			if o.Disk {
				dt, derr := s.DiskTree(ref)
				if derr != nil {
					return derr
				}
				d, err = s.Git("diff", "-M", ref, dt)
			} else {
				from, ferr := s.diffBase(ref)
				if ferr != nil {
					return ferr
				}
				d, err = s.Git("diff", "-M", from, ref)
			}
			if err != nil {
				return err
			}
			if d != "" {
				fmt.Fprintf(out, "=== %s ===\n%s\n", ShortName(ref), d)
			}
		}
		return nil
	}
	ref, err := s.ResolveRef(o.Name)
	if err != nil {
		return err
	}
	if o.Disk {
		rev := o.Rev1
		if rev == "" {
			rev = ref
		}
		dt, err := s.DiskTree(rev)
		if err != nil {
			return err
		}
		d, err := s.Git("diff", "-M", rev, dt)
		if err != nil {
			return err
		}
		if d != "" {
			fmt.Fprintln(out, d)
		}
		return nil
	}
	from := o.Rev1
	if from == "" {
		if from, err = s.diffBase(ref); err != nil {
			return err
		}
	}
	to := o.Rev2
	if to == "" {
		to = ref
	}
	d, err := s.Git("diff", "-M", from, to)
	if err != nil {
		return err
	}
	if d != "" {
		fmt.Fprintln(out, d)
	}
	return nil
}

// diffBase is a ref's parent, or the empty tree for single-version histories.
func (s *Store) diffBase(ref string) (string, error) {
	if parent, ok := s.RevParse(ref + "~1"); ok {
		return parent, nil
	}
	return s.EmptyTree()
}

// RestoreRef writes a revision's files back to their recorded paths.
func (s *Store) RestoreRef(ref, rev string, out io.Writer) error {
	entries, err := s.TreeEntries(rev)
	if err != nil {
		return err
	}
	paths := make([]string, 0, len(entries))
	for p := range entries {
		paths = append(paths, p)
	}
	sort.Strings(paths)
	for _, p := range paths {
		e := entries[p]
		dest := s.DiskPath(p)
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return err
		}
		content, err := s.GitRaw("cat-file", "blob", e.SHA)
		if err != nil {
			return err
		}
		perm := os.FileMode(0o644)
		if e.Mode == "100755" {
			perm = 0o755
		}
		if err := os.WriteFile(dest, content, perm); err != nil {
			return err
		}
		// WriteFile does not chmod pre-existing files
		os.Chmod(dest, perm)
		fmt.Fprintf(out, "restored %s @ %s\n", dest, s.Short(rev))
	}
	return nil
}

// Restore writes a history (or with all=true, every history) back to disk.
func (s *Store) Restore(name, rev string, all bool, out io.Writer) error {
	if all {
		for _, ref := range s.ForEachRef("refs/mess") {
			if err := s.RestoreRef(ref, ref, out); err != nil {
				return err
			}
		}
		return nil
	}
	ref, err := s.ResolveRef(name)
	if err != nil {
		return err
	}
	if rev == "" {
		rev = ref
	}
	return s.RestoreRef(ref, rev, out)
}

// Move renames a history. If the history's name maps to a real file on
// disk, the file moves too and the rename is recorded as a new version.
func (s *Store) Move(oldName, newName string, out io.Writer) error {
	oldRef, err := s.ResolveRef(oldName)
	if err != nil {
		return err
	}
	paths, err := s.TreePaths(oldRef)
	if err != nil {
		return err
	}
	didMove := false
	src := ""
	if len(paths) == 1 {
		src = s.DiskPath(ShortName(oldRef))
		if fi, err := os.Stat(src); err == nil && fi.Mode().IsRegular() {
			if _, err := os.Stat(newName); os.IsNotExist(err) {
				if err := os.MkdirAll(filepath.Dir(newName), 0o755); err != nil {
					return err
				}
				if err := os.Rename(src, newName); err != nil {
					return err
				}
				didMove = true
			}
		}
	}
	rollback := func() {
		if didMove {
			os.Rename(newName, src)
		}
	}
	newRef, err := s.NameToRef(newName)
	if err != nil {
		rollback()
		return err
	}
	if newRef == oldRef {
		rollback()
		return fmt.Errorf("git-mess: same history name")
	}
	if _, ok := s.RevParse(newRef); ok {
		rollback()
		return fmt.Errorf("git-mess: history already exists: %s", newName)
	}
	tip, _ := s.RevParse(oldRef)
	if _, err := s.Git("update-ref", newRef, tip); err != nil {
		rollback()
		return err
	}
	if _, err := s.Git("update-ref", "-d", oldRef); err != nil {
		return err
	}
	if didMove {
		fmt.Fprintf(out, "moved file: %s -> %s\n", src, newName)
	}
	fmt.Fprintf(out, "moved: %s -> %s\n", ShortName(oldRef), ShortName(newRef))
	if didMove {
		// record a version under the new path so history matches the disk
		abs, err := filepath.Abs(newName)
		if err != nil {
			return err
		}
		tp := s.TreePath(abs)
		blob, err := s.Git("rev-parse", newRef+":"+paths[0])
		if err != nil {
			return err
		}
		mode, err := fileModeOf(abs)
		if err != nil {
			return err
		}
		tree, err := s.buildTree([]indexEntry{{mode, blob, tp}})
		if err != nil {
			return err
		}
		msg := fmt.Sprintf("move: %s -> %s", ShortName(oldRef), ShortName(newRef))
		commit, err := s.Git("commit-tree", tree, "-m", msg, "-p", tip)
		if err != nil {
			return err
		}
		if _, err := s.Git("update-ref", newRef, commit); err != nil {
			return err
		}
	}
	return nil
}

// Delete drops a history, leaving a tombstone so peers delete it too.
// With prune, unreachable objects are purged from the store immediately.
func (s *Store) Delete(name string, prune bool, out io.Writer) error {
	ref, err := s.ResolveRef(name)
	if err != nil {
		return err
	}
	tip, _ := s.RevParse(ref)
	if _, err := s.Git("update-ref", "-d", ref); err != nil {
		return err
	}
	// tombstone: an empty-tree commit NOT parented on the old chain, so the
	// chain stays purgeable; it records that the deletion happened and when
	empty, err := s.EmptyTree()
	if err != nil {
		return err
	}
	ts, err := s.Git("commit-tree", empty, "-m",
		fmt.Sprintf("tombstone: %s (was %s)", ShortName(ref), tip))
	if err != nil {
		return err
	}
	if _, err := s.Git("update-ref", "refs/mess-tombstones/"+ShortName(ref), ts); err != nil {
		return err
	}
	fmt.Fprintf(out, "deleted history: %s (tombstoned — deletion propagates on push)\n", name)
	if prune {
		if _, err := s.Git("reflog", "expire", "--expire=now", "--all"); err != nil {
			return err
		}
		if _, err := s.Git("gc", "-q", "--prune=now"); err != nil {
			return err
		}
		fmt.Fprintf(out, "pruned unreachable objects from %s\n", s.GitDir)
	} else {
		fmt.Fprintln(out, "(objects remain until 'git mess delete --prune' or gc)")
	}
	return nil
}

// Untracked lists files under dir (default: store root, or cwd for the
// global store) that no history's latest version records. Subtrees owned
// by another mess and .git directories are skipped.
func (s *Store) Untracked(dir string, out io.Writer) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	if dir == "" {
		if s.Root != "" {
			dir = s.Root
		} else {
			var err error
			if dir, err = os.Getwd(); err != nil {
				return err
			}
		}
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return fmt.Errorf("git-mess: no such directory: %s", dir)
	}
	tracked := map[string]bool{}
	for _, ref := range s.ForEachRef("refs/mess") {
		paths, err := s.TreePaths(ref)
		if err != nil {
			continue
		}
		for _, p := range paths {
			tracked[p] = true
		}
	}
	return filepath.WalkDir(abs, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if p == abs {
				return nil
			}
			if d.Name() == ".git" || d.Name() == ".git-mess.git" {
				return fs.SkipDir
			}
			if fi, err := os.Stat(filepath.Join(p, ".git-mess.git")); err == nil && fi.IsDir() {
				return fs.SkipDir
			}
			return nil
		}
		if !d.Type().IsRegular() {
			return nil
		}
		if !tracked[s.TreePath(p)] {
			fmt.Fprintln(out, p)
		}
		return nil
	})
}

// InitLocal creates a local store in dir, warning if dir is inside a
// regular git repository.
func InitLocal(dir string, out, errOut io.Writer) error {
	if dir == "" {
		dir = "."
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return err
	}
	if fi, err := os.Stat(abs); err != nil || !fi.IsDir() {
		return fmt.Errorf("git-mess: no such directory: %s", dir)
	}
	target := filepath.Join(abs, ".git-mess.git")
	if _, err := os.Stat(target); err == nil {
		return fmt.Errorf("git-mess: store already exists: %s", target)
	}
	if _, err := RunGit("", "-C", abs, "rev-parse", "--git-dir"); err == nil {
		fmt.Fprintf(errOut, "warning: %s is inside a regular git repository\n", abs)
		fmt.Fprintln(errOut, "         the two coexist, but consider adding .git-mess.git/ to .gitignore")
		fmt.Fprintln(errOut, "         and avoid snapshotting files that repo already tracks")
	}
	if _, err := RunGit("", "init", "-q", "--bare", target); err != nil {
		return err
	}
	fmt.Fprintf(out, "created local store: %s\n", target)
	fmt.Fprintf(out, "(used automatically by git mess anywhere under %s)\n", abs)
	return nil
}

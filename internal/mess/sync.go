package mess

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
)

// HubInit creates a shared store: an ordinary bare repository configured
// fast-forward-only (rewrites denied) with ref deletions allowed, so
// tombstoned deletes can propagate.
func HubInit(path string, out io.Writer) error {
	if _, err := os.Stat(path); err == nil {
		return fmt.Errorf("git-mess: destination exists: %s", path)
	}
	if _, err := RunGit("", "init", "-q", "--bare", path); err != nil {
		return err
	}
	for k, v := range map[string]string{
		"receive.denyNonFastForwards": "true",
		"receive.denyDeletes":         "false",
	} {
		if _, err := RunGit("", "--git-dir", path, "config", k, v); err != nil {
			return err
		}
	}
	fmt.Fprintf(out, "hub store created: %s\n", path)
	fmt.Fprintln(out, "  history rewrites: DENIED (fast-forward only — nobody can erase a peer's versions)")
	fmt.Fprintln(out, "  ref deletions:    ALLOWED (tombstoned deletes propagate; run gc there to purge)")
	fmt.Fprintln(out, "share it, then:  git mess push <hub>   /   git mess pull <hub>")
	return nil
}

// Clone copies a remote mess into dir and materializes every history.
func Clone(url, dir string, out io.Writer) (*Store, error) {
	if dir == "" {
		dir = strings.TrimSuffix(filepath.Base(url), ".git")
		if dir == "" {
			dir = "mess"
		}
	}
	if _, err := os.Stat(dir); err == nil {
		return nil, fmt.Errorf("git-mess: destination exists: %s", dir)
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, err
	}
	abs, err := filepath.Abs(dir)
	if err != nil {
		return nil, err
	}
	s := &Store{GitDir: filepath.Join(abs, ".git-mess.git"), Root: abs}
	if _, err := RunGit("", "init", "-q", "--bare", s.GitDir); err != nil {
		return nil, err
	}
	if _, err := s.Git("fetch", "-q", url,
		"refs/mess/*:refs/mess/*", "refs/mess-tombstones/*:refs/mess-tombstones/*"); err != nil {
		return nil, err
	}
	count := 0
	for _, ref := range s.ForEachRef("refs/mess") {
		if err := s.RestoreRef(ref, ref, out); err != nil {
			return nil, err
		}
		count++
	}
	fmt.Fprintf(out, "cloned %d histories into %s\n", count, dir)
	return s, nil
}

// Push publishes histories, tombstones, and deletions to a remote. With a
// name, only that history (or its pending deletion) is pushed.
func (s *Store) Push(remote, only string, out, errOut io.Writer) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	var specs []string
	if only != "" {
		ref, err := s.NameToRef(only)
		if err != nil {
			return err
		}
		name := ShortName(ref)
		_, hasRef := s.RevParse(ref)
		_, hasTomb := s.RevParse("refs/mess-tombstones/" + name)
		switch {
		case hasRef:
			specs = append(specs, ref+":"+ref)
		case hasTomb:
			specs = append(specs, ":refs/mess/"+name)
		default:
			return fmt.Errorf("git-mess: no history or tombstone: %s", only)
		}
		if hasTomb {
			specs = append(specs, "refs/mess-tombstones/"+name+":refs/mess-tombstones/"+name)
		}
	} else {
		specs = append(specs, "refs/mess/*:refs/mess/*", "refs/mess-tombstones/*:refs/mess-tombstones/*")
		// propagate deletions: tombstoned names that still exist on the remote
		remoteRefs := map[string]bool{}
		if lsr, err := s.Git("ls-remote", remote, "refs/mess/*"); err == nil {
			for _, line := range strings.Split(lsr, "\n") {
				if _, ref, ok := strings.Cut(line, "\t"); ok {
					remoteRefs[ref] = true
				}
			}
		}
		for _, t := range s.ForEachRef("refs/mess-tombstones") {
			name := strings.TrimPrefix(t, "refs/mess-tombstones/")
			if remoteRefs["refs/mess/"+name] {
				specs = append(specs, ":refs/mess/"+name)
			}
		}
	}
	cmd := exec.Command("git", append([]string{"--git-dir", s.GitDir, "push", remote}, specs...)...)
	cmd.Stdout = out
	cmd.Stderr = out
	if err := cmd.Run(); err != nil {
		fmt.Fprintln(errOut, "git-mess: push rejected — someone else pushed first.")
		fmt.Fprintf(errOut, "          run 'git mess pull %s' to merge, then push again\n", remote)
		return fmt.Errorf("push failed")
	}
	return nil
}

// Pull fetches from a remote and reconciles each history: adopt, fast-
// forward, 3-way merge, or apply/propagate tombstones. With a name, only
// that history is reconciled.
func (s *Store) Pull(remote, only string, out io.Writer) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	if only != "" {
		ref, err := s.NameToRef(only)
		if err != nil {
			return err
		}
		only = ShortName(ref)
	}
	s.clearNS("refs/mess-incoming")
	s.clearNS("refs/mess-tombstones-incoming")
	defer s.clearNS("refs/mess-incoming")
	defer s.clearNS("refs/mess-tombstones-incoming")
	if _, err := s.Git("fetch", "-q", remote,
		"+refs/mess/*:refs/mess-incoming/*",
		"+refs/mess-tombstones/*:refs/mess-tombstones-incoming/*"); err != nil {
		return err
	}
	names := map[string]bool{}
	for _, r := range s.ForEachRef("refs/mess-incoming") {
		names[strings.TrimPrefix(r, "refs/mess-incoming/")] = true
	}
	for _, r := range s.ForEachRef("refs/mess-tombstones-incoming") {
		names[strings.TrimPrefix(r, "refs/mess-tombstones-incoming/")] = true
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, name := range sorted {
		if only != "" && name != only {
			continue
		}
		if err := s.pullOne(name, out); err != nil {
			return err
		}
	}
	return nil
}

func (s *Store) clearNS(ns string) {
	for _, r := range s.ForEachRef(ns) {
		s.Git("update-ref", "-d", r)
	}
}

func (s *Store) pullOne(name string, out io.Writer) error {
	R, rOk := s.RevParse("refs/mess-incoming/" + name)
	Ti, tiOk := s.RevParse("refs/mess-tombstones-incoming/" + name)
	L, lOk := s.RevParse("refs/mess/" + name)
	T, tOk := s.RevParse("refs/mess-tombstones/" + name)
	ref := "refs/mess/" + name

	// the remote's latest word is "deleted" if its tombstone postdates its snapshot
	if tiOk && (!rOk || s.Epoch(Ti) > s.Epoch(R)) {
		if lOk && s.Epoch(L) > s.Epoch(Ti) {
			fmt.Fprintf(out, "%s: remote deleted it, but local is newer — keeping (push to revive remotely)\n", name)
			return nil
		}
		if lOk {
			if _, err := s.Git("update-ref", "-d", ref); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: deleted (remote tombstone; files left on disk)\n", name)
		}
		_, err := s.Git("update-ref", "refs/mess-tombstones/"+name, Ti)
		return err
	}
	if !rOk {
		return nil
	}
	if tOk {
		if s.Epoch(R) > s.Epoch(T) {
			if _, err := s.Git("update-ref", "-d", "refs/mess-tombstones/"+name); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: revived by newer remote version\n", name)
		} else {
			fmt.Fprintf(out, "%s: deleted locally (tombstone is newer) — push will propagate the deletion\n", name)
			return nil
		}
	}

	switch {
	case !lOk:
		if _, err := s.Git("update-ref", ref, R); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: new -> %s\n", name, s.Short(R))
		return s.syncRestore(ref, out)
	case L == R:
		fmt.Fprintf(out, "%s: up to date\n", name)
		return nil
	case s.mergeBase(L, R) == L:
		if s.IsDirty(ref) {
			fmt.Fprintf(out, "%s: SKIPPED — unsnapshotted local changes (snapshot or restore, then pull again)\n", name)
			return nil
		}
		if _, err := s.Git("update-ref", ref, R); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: fast-forward -> %s\n", name, s.Short(R))
		return s.RestoreRef(ref, ref, out)
	case s.mergeBase(L, R) == R:
		fmt.Fprintf(out, "%s: local is ahead (push to publish)\n", name)
		return nil
	default:
		if s.IsDirty(ref) {
			fmt.Fprintf(out, "%s: SKIPPED — unsnapshotted local changes (snapshot or restore, then pull again)\n", name)
			return nil
		}
		return s.mergeHistory(name, R, out)
	}
}

func (s *Store) mergeBase(a, b string) string {
	out, err := s.Git("merge-base", a, b)
	if err != nil {
		return ""
	}
	return out
}

// syncRestore restores a ref's tip unless differing files are in the way.
func (s *Store) syncRestore(ref string, out io.Writer) error {
	entries, err := s.TreeEntries(ref)
	if err != nil {
		return err
	}
	for p, e := range entries {
		dest := s.DiskPath(p)
		if fi, err := os.Stat(dest); err == nil && fi.Mode().IsRegular() {
			h, err := s.Git("hash-object", dest)
			if err != nil {
				return err
			}
			if h != e.SHA {
				fmt.Fprintf(out, "  (files differ on disk — not restoring; 'git mess restore %s' to overwrite)\n",
					ShortName(ref))
				return nil
			}
		}
	}
	return s.RestoreRef(ref, ref, out)
}

// mergeHistory three-way-merges a diverged history: common ancestor from
// merge-base, per-file content merge via git merge-file. Conflicts are
// committed WITH markers so the graph converges; the user edits and
// re-snapshots to resolve.
func (s *Store) mergeHistory(name, remoteSHA string, out io.Writer) error {
	ref := "refs/mess/" + name
	lo, _ := s.RevParse(ref)
	base := s.mergeBase(lo, remoteSHA)

	le, err := s.TreeEntries(lo)
	if err != nil {
		return err
	}
	re, err := s.TreeEntries(remoteSHA)
	if err != nil {
		return err
	}
	be := map[string]TreeEntry{}
	if base != "" {
		if be, err = s.TreeEntries(base); err != nil {
			return err
		}
	}
	pathSet := map[string]bool{}
	for p := range le {
		pathSet[p] = true
	}
	for p := range re {
		pathSet[p] = true
	}
	for p := range be {
		pathSet[p] = true
	}
	paths := make([]string, 0, len(pathSet))
	for p := range pathSet {
		paths = append(paths, p)
	}
	sort.Strings(paths)

	var entries []indexEntry
	var conflicts []string
	for _, p := range paths {
		bl, br, bb := le[p].SHA, re[p].SHA, be[p].SHA
		var blob string
		switch {
		case bl == br:
			blob = bl // same content (or absent) on both sides
		case bb == bl:
			blob = br // only they changed
		case bb == br:
			blob = bl // only we changed
		case bl == "" || br == "":
			blob = bl // modify vs delete: keep the surviving file
			if blob == "" {
				blob = br
			}
			conflicts = append(conflicts, p+" (modify/delete)")
		default:
			merged, conflicted, err := s.mergeBlobs(bl, bb, br)
			if err != nil {
				return err
			}
			if conflicted {
				conflicts = append(conflicts, p)
			}
			blob = merged
		}
		if blob == "" {
			continue
		}
		mode := le[p].Mode
		if mode == "" {
			mode = re[p].Mode
		}
		if mode == "" {
			mode = "100644"
		}
		entries = append(entries, indexEntry{mode, blob, p})
	}
	tree, err := s.buildTree(entries)
	if err != nil {
		return err
	}
	commit, err := s.Git("commit-tree", tree, "-m", "merge: "+name, "-p", lo, "-p", remoteSHA)
	if err != nil {
		return err
	}
	if _, err := s.Git("update-ref", ref, commit); err != nil {
		return err
	}
	if err := s.RestoreRef(ref, ref, io.Discard); err != nil {
		return err
	}
	if len(conflicts) > 0 {
		fmt.Fprintf(out, "%s -> %s  MERGED WITH CONFLICTS:\n", name, s.Short(commit))
		for _, c := range conflicts {
			fmt.Fprintf(out, "    CONFLICT: %s\n", c)
		}
		fmt.Fprintln(out, "    markers are in the files AND recorded — edit, then 'git mess snapshot' to resolve")
	} else {
		fmt.Fprintf(out, "%s -> %s  (merged)\n", name, s.Short(commit))
	}
	return nil
}

// mergeBlobs runs git merge-file over three blob versions (base may be "")
// and returns the resulting blob and whether it contains conflict markers.
func (s *Store) mergeBlobs(ours, base, theirs string) (string, bool, error) {
	dir, err := os.MkdirTemp("", "git-mess-merge")
	if err != nil {
		return "", false, err
	}
	defer os.RemoveAll(dir)
	write := func(fname, blob string) (string, error) {
		p := filepath.Join(dir, fname)
		var content []byte
		if blob != "" {
			if content, err = s.GitRaw("cat-file", "blob", blob); err != nil {
				return "", err
			}
		}
		return p, os.WriteFile(p, content, 0o644)
	}
	fOurs, err := write("ours", ours)
	if err != nil {
		return "", false, err
	}
	fBase, err := write("base", base)
	if err != nil {
		return "", false, err
	}
	fTheirs, err := write("theirs", theirs)
	if err != nil {
		return "", false, err
	}
	cmd := exec.Command("git", "merge-file", "-p",
		"-L", "ours", "-L", "base", "-L", "theirs", fOurs, fBase, fTheirs)
	merged, err := cmd.Output()
	conflicted := false
	if err != nil {
		if _, isExit := err.(*exec.ExitError); !isExit {
			return "", false, err
		}
		conflicted = true // merge-file exits with the number of conflicts
	}
	blob, err := s.GitStdin(string(merged), "hash-object", "-w", "--stdin")
	return blob, conflicted, err
}

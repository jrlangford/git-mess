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

// Remote manages named remotes, stored as ordinary git remote config in
// the store. No args lists them; "add <name> <url>" and "remove <name>"
// modify them.
func (s *Store) Remote(args []string, out io.Writer) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	switch {
	case len(args) == 0:
		res, err := s.Git("remote", "-v")
		if err != nil {
			return err
		}
		if res != "" {
			fmt.Fprintln(out, res)
		}
		return nil
	case args[0] == "add" && len(args) == 3:
		if _, err := s.Git("remote", "add", args[1], args[2]); err != nil {
			return err
		}
		fmt.Fprintf(out, "remote added: %s -> %s\n", args[1], args[2])
		return nil
	case (args[0] == "remove" || args[0] == "rm") && len(args) == 2:
		if _, err := s.Git("remote", "remove", args[1]); err != nil {
			return err
		}
		fmt.Fprintf(out, "remote removed: %s\n", args[1])
		return nil
	default:
		return fmt.Errorf("usage: git mess remote [add <name> <url> | remove <name>]")
	}
}

// defaultRemote resolves an empty remote argument to "origin" if configured.
func (s *Store) defaultRemote(remote string) (string, error) {
	if remote != "" {
		return remote, nil
	}
	res, _ := s.Git("remote")
	for _, r := range strings.Split(res, "\n") {
		if r == "origin" {
			return "origin", nil
		}
	}
	return "", fmt.Errorf("git-mess: no remote given and no 'origin' configured — git mess remote add origin <url>")
}

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
		"refs/mess/*:refs/mess/*",
		"refs/mess-archive/*:refs/mess-archive/*",
		"refs/mess-tombstones/*:refs/mess-tombstones/*"); err != nil {
		return nil, err
	}
	if _, err := s.Git("remote", "add", "origin", url); err != nil {
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
	var rerr error
	if remote, rerr = s.defaultRemote(remote); rerr != nil {
		return rerr
	}
	remoteRefs := map[string]string{} // ref -> sha
	if lsr, err := s.Git("ls-remote", remote, "refs/mess/*", "refs/mess-archive/*"); err == nil {
		for _, line := range strings.Split(lsr, "\n") {
			if sha, ref, ok := strings.Cut(line, "\t"); ok {
				remoteRefs[ref] = sha
			}
		}
	}
	// canRetire: deleting a remote ref must itself obey newest-wins — only
	// when our lifecycle event postdates the remote's sha, and only when we
	// actually hold that sha (otherwise we haven't seen their latest: skip,
	// and let the hub's fast-forward rule reject anything stale we push).
	canRetire := func(localEvent, remoteRef string) bool {
		sha, exists := remoteRefs[remoteRef]
		if !exists {
			return false
		}
		if _, err := s.Git("cat-file", "-e", sha); err == nil {
			return s.Epoch(localEvent) >= s.Epoch(sha)
		}
		// we no longer hold the remote's object (e.g. purged by delete
		// --prune); retire only if our lifecycle event explicitly recorded
		// that exact sha — proof we knowingly disposed of that state
		msg, _ := s.Git("log", "-1", "--format=%B", localEvent)
		return strings.Contains(msg, sha)
	}
	var specs []string
	if only != "" {
		ref, err := s.NameToRef(only)
		if err != nil {
			return err
		}
		name := ShortName(ref)
		_, hasRef := s.RevParse(ref)
		_, hasArch := s.RevParse("refs/mess-archive/" + name)
		_, hasTomb := s.RevParse("refs/mess-tombstones/" + name)
		activeTip, _ := s.RevParse(ref)
		archTip, _ := s.RevParse("refs/mess-archive/" + name)
		tomb, _ := s.RevParse("refs/mess-tombstones/" + name)
		switch {
		case hasRef:
			specs = append(specs, ref+":"+ref)
			if canRetire(activeTip, "refs/mess-archive/"+name) {
				specs = append(specs, ":refs/mess-archive/"+name) // unarchive propagates
			}
		case hasArch:
			specs = append(specs, "refs/mess-archive/"+name+":refs/mess-archive/"+name)
			if canRetire(archTip, "refs/mess/"+name) {
				specs = append(specs, ":refs/mess/"+name) // archive propagates
			}
		case hasTomb:
			if canRetire(tomb, "refs/mess/"+name) {
				specs = append(specs, ":refs/mess/"+name)
			}
			if canRetire(tomb, "refs/mess-archive/"+name) {
				specs = append(specs, ":refs/mess-archive/"+name)
			}
		default:
			return fmt.Errorf("git-mess: no history, archive, or tombstone: %s", only)
		}
		if hasTomb {
			specs = append(specs, "refs/mess-tombstones/"+name+":refs/mess-tombstones/"+name)
		}
	} else {
		specs = append(specs,
			"refs/mess/*:refs/mess/*",
			"refs/mess-archive/*:refs/mess-archive/*",
			"refs/mess-tombstones/*:refs/mess-tombstones/*")
		// propagate deletions and lifecycle transitions for names whose
		// remote refs are now outdated namespaces
		for _, t := range s.ForEachRef("refs/mess-tombstones") {
			name := strings.TrimPrefix(t, "refs/mess-tombstones/")
			tomb, _ := s.RevParse(t)
			if canRetire(tomb, "refs/mess/"+name) {
				specs = append(specs, ":refs/mess/"+name)
			}
			if canRetire(tomb, "refs/mess-archive/"+name) {
				specs = append(specs, ":refs/mess-archive/"+name)
			}
		}
		for _, a := range s.ForEachRef("refs/mess-archive") {
			name := strings.TrimPrefix(a, "refs/mess-archive/")
			marker, _ := s.RevParse(a)
			if canRetire(marker, "refs/mess/"+name) {
				specs = append(specs, ":refs/mess/"+name) // archived here: retire remote active
			}
		}
		for _, r := range s.ForEachRef("refs/mess") {
			name := ShortName(r)
			tip, _ := s.RevParse(r)
			if canRetire(tip, "refs/mess-archive/"+name) {
				specs = append(specs, ":refs/mess-archive/"+name) // active here: retire remote archive
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
	var rerr error
	if remote, rerr = s.defaultRemote(remote); rerr != nil {
		return rerr
	}
	if only != "" {
		ref, err := s.NameToRef(only)
		if err != nil {
			return err
		}
		only = ShortName(ref)
	}
	for _, ns := range []string{"refs/mess-incoming", "refs/mess-archive-incoming", "refs/mess-tombstones-incoming"} {
		s.clearNS(ns)
		defer s.clearNS(ns)
	}
	if _, err := s.Git("fetch", "-q", remote,
		"+refs/mess/*:refs/mess-incoming/*",
		"+refs/mess-archive/*:refs/mess-archive-incoming/*",
		"+refs/mess-tombstones/*:refs/mess-tombstones-incoming/*"); err != nil {
		return err
	}
	names := map[string]bool{}
	for _, ns := range []string{"refs/mess-incoming/", "refs/mess-archive-incoming/", "refs/mess-tombstones-incoming/"} {
		for _, r := range s.ForEachRef(strings.TrimSuffix(ns, "/")) {
			names[strings.TrimPrefix(r, ns)] = true
		}
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

// syncState classifies one history's relationship to a remote's state. The
// SAME classification drives pull (which acts) and fetch (which previews),
// so the preview can never drift from the action.
type syncState int

const (
	stateNone syncState = iota
	stateRemoteDeleted
	stateRemoteDeletedLocalNewer
	stateLocallyDeleted
	stateNew
	stateUpToDate
	stateFastForward
	stateLocalAhead
	stateDiverged
	stateRemoteArchived
	stateRemoteArchivedLocalNewer
	stateLocallyArchived
	stateArchiveUpToDate
	stateArchiveFastForward
	stateArchiveLocalAhead
	stateArchiveDiverged
)

// sideState is one name's refs on one side (local, or a remote's fetched
// copy): active history, archive, tombstone.
type sideState struct {
	Active, Archive, Tomb          string
	hasActive, hasArchive, hasTomb bool
}

func (s *Store) sideOf(activeNS, archiveNS, tombNS, name string) sideState {
	var st sideState
	st.Active, st.hasActive = s.RevParse(activeNS + name)
	st.Archive, st.hasArchive = s.RevParse(archiveNS + name)
	st.Tomb, st.hasTomb = s.RevParse(tombNS + name)
	return st
}

// classify compares one name's remote state against its local state under
// the newest-event-wins rule, across the full lifecycle: active snapshots,
// archive markers, and deletion tombstones. revive means a local tombstone
// is outdated and should be dropped first; reactivate means a local archive
// is outdated by newer remote activity and should return to active first.
func (s *Store) classify(rem, loc sideState) (reactivate, revive bool, st syncState) {
	var eR, eRa, eTi, eL, eA, eT int64
	if rem.hasActive {
		eR = s.Epoch(rem.Active)
	}
	if rem.hasArchive {
		eRa = s.Epoch(rem.Archive)
	}
	if rem.hasTomb {
		eTi = s.Epoch(rem.Tomb)
	}
	if loc.hasActive {
		eL = s.Epoch(loc.Active)
	}
	if loc.hasArchive {
		eA = s.Epoch(loc.Archive)
	}
	if loc.hasTomb {
		eT = s.Epoch(loc.Tomb)
	}

	switch {
	case !rem.hasActive && !rem.hasArchive && !rem.hasTomb:
		return false, false, stateNone

	// the remote's latest word is "deleted"
	case rem.hasTomb && eTi >= eR && eTi >= eRa:
		if (loc.hasActive && eL > eTi) || (loc.hasArchive && eA > eTi) {
			return false, false, stateRemoteDeletedLocalNewer
		}
		return false, false, stateRemoteDeleted

	// the remote's latest word is "archived"
	case rem.hasArchive && eRa >= eR:
		if loc.hasTomb {
			if eT >= eRa {
				return false, false, stateLocallyDeleted
			}
			revive = true
		}
		if loc.hasActive {
			if eL > eRa {
				return false, revive, stateRemoteArchivedLocalNewer
			}
			return false, revive, stateRemoteArchived
		}
		if loc.hasArchive {
			switch {
			case loc.Archive == rem.Archive:
				return false, revive, stateArchiveUpToDate
			case s.mergeBase(loc.Archive, rem.Archive) == loc.Archive:
				return false, revive, stateArchiveFastForward
			case s.mergeBase(loc.Archive, rem.Archive) == rem.Archive:
				return false, revive, stateArchiveLocalAhead
			default:
				return false, revive, stateArchiveDiverged
			}
		}
		return false, revive, stateRemoteArchived

	// the remote's latest word is an active snapshot
	default:
		if loc.hasTomb {
			if eT >= eR {
				return false, false, stateLocallyDeleted
			}
			revive = true
		}
		effL, hasEff := loc.Active, loc.hasActive
		if loc.hasArchive && !loc.hasActive {
			if eA >= eR {
				return false, revive, stateLocallyArchived
			}
			reactivate = true
			effL, hasEff = loc.Archive, true
		}
		switch {
		case !hasEff:
			return reactivate, revive, stateNew
		case effL == rem.Active:
			return reactivate, revive, stateUpToDate
		case s.mergeBase(effL, rem.Active) == effL:
			return reactivate, revive, stateFastForward
		case s.mergeBase(effL, rem.Active) == rem.Active:
			return reactivate, revive, stateLocalAhead
		default:
			return reactivate, revive, stateDiverged
		}
	}
}

func (s *Store) pullOne(name string, out io.Writer) error {
	rem := s.sideOf("refs/mess-incoming/", "refs/mess-archive-incoming/", "refs/mess-tombstones-incoming/", name)
	loc := s.sideOf("refs/mess/", "refs/mess-archive/", "refs/mess-tombstones/", name)
	ref := "refs/mess/" + name
	aRef := "refs/mess-archive/" + name

	reactivate, revive, st := s.classify(rem, loc)
	if revive {
		if _, err := s.Git("update-ref", "-d", "refs/mess-tombstones/"+name); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: revived by newer remote version\n", name)
	}
	if reactivate {
		if _, err := s.Git("update-ref", ref, loc.Archive); err != nil {
			return err
		}
		s.Git("update-ref", "-d", aRef)
		fmt.Fprintf(out, "%s: unarchived (remote has newer activity)\n", name)
		loc.hasActive, loc.Active = true, loc.Archive
	}
	switch st {
	case stateNone:
		return nil
	case stateRemoteDeletedLocalNewer:
		fmt.Fprintf(out, "%s: remote deleted it, but local is newer — keeping (push to revive remotely)\n", name)
		return nil
	case stateRemoteDeleted:
		if loc.hasActive {
			if _, err := s.Git("update-ref", "-d", ref); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: deleted (remote tombstone; files left on disk)\n", name)
		}
		if loc.hasArchive {
			if _, err := s.Git("update-ref", "-d", aRef); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: archived copy deleted (remote tombstone)\n", name)
		}
		_, err := s.Git("update-ref", "refs/mess-tombstones/"+name, rem.Tomb)
		return err
	case stateLocallyDeleted:
		fmt.Fprintf(out, "%s: deleted locally (tombstone is newer) — push will propagate the deletion\n", name)
		return nil
	case stateRemoteArchivedLocalNewer:
		fmt.Fprintf(out, "%s: remote archived it, but local is newer — keeping (push to restore remotely)\n", name)
		return nil
	case stateRemoteArchived:
		if loc.hasActive {
			if _, err := s.Git("update-ref", "-d", ref); err != nil {
				return err
			}
			fmt.Fprintf(out, "%s: archived (remote archive; files left on disk)\n", name)
		} else {
			fmt.Fprintf(out, "%s: archived history adopted\n", name)
		}
		_, err := s.Git("update-ref", aRef, rem.Archive)
		return err
	case stateArchiveUpToDate:
		fmt.Fprintf(out, "%s: archive up to date\n", name)
		return nil
	case stateArchiveFastForward:
		if _, err := s.Git("update-ref", aRef, rem.Archive); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: archive updated -> %s\n", name, s.Short(rem.Archive))
		return nil
	case stateArchiveLocalAhead:
		fmt.Fprintf(out, "%s: local archive is ahead (push to publish)\n", name)
		return nil
	case stateArchiveDiverged:
		fmt.Fprintf(out, "%s: archived versions diverged — unarchive to reconcile\n", name)
		return nil
	case stateNew:
		if _, err := s.Git("update-ref", ref, rem.Active); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: new -> %s\n", name, s.Short(rem.Active))
		return s.syncRestore(ref, out)
	case stateUpToDate:
		fmt.Fprintf(out, "%s: up to date\n", name)
		return nil
	case stateFastForward:
		if s.IsDirty(ref) {
			fmt.Fprintf(out, "%s: SKIPPED — unsnapshotted local changes (snapshot or restore, then pull again)\n", name)
			return nil
		}
		if _, err := s.Git("update-ref", ref, rem.Active); err != nil {
			return err
		}
		fmt.Fprintf(out, "%s: fast-forward -> %s\n", name, s.Short(rem.Active))
		return s.RestoreRef(ref, ref, out)
	case stateLocalAhead:
		fmt.Fprintf(out, "%s: local is ahead (push to publish)\n", name)
		return nil
	default: // stateDiverged
		if s.IsDirty(ref) {
			fmt.Fprintf(out, "%s: SKIPPED — unsnapshotted local changes (snapshot or restore, then pull again)\n", name)
			return nil
		}
		return s.mergeHistory(name, rem.Active, out)
	}
}

// Fetch downloads remote state into refs/mess-fetched/* (and its tombstone
// namespace) without touching histories or files, and previews what pull
// would do with each name.
func (s *Store) Fetch(remote, only string, out io.Writer) error {
	if err := s.Ensure(); err != nil {
		return err
	}
	var rerr error
	if remote, rerr = s.defaultRemote(remote); rerr != nil {
		return rerr
	}
	if only != "" {
		ref, err := s.NameToRef(only)
		if err != nil {
			return err
		}
		only = ShortName(ref)
	}
	s.clearNS("refs/mess-fetched")
	s.clearNS("refs/mess-archive-fetched")
	s.clearNS("refs/mess-tombstones-fetched")
	if _, err := s.Git("fetch", "-q", remote,
		"+refs/mess/*:refs/mess-fetched/*",
		"+refs/mess-archive/*:refs/mess-archive-fetched/*",
		"+refs/mess-tombstones/*:refs/mess-tombstones-fetched/*"); err != nil {
		return err
	}
	names := map[string]bool{}
	for _, ns := range []string{"refs/mess-fetched/", "refs/mess-archive-fetched/", "refs/mess-tombstones-fetched/"} {
		for _, r := range s.ForEachRef(strings.TrimSuffix(ns, "/")) {
			names[strings.TrimPrefix(r, ns)] = true
		}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	preview := map[syncState]string{
		stateRemoteDeleted:            "deleted on remote (pull will delete locally)",
		stateRemoteDeletedLocalNewer:  "deleted on remote, but local is newer (pull keeps yours)",
		stateLocallyDeleted:           "deleted locally (push will propagate the deletion)",
		stateNew:                      "new (pull will adopt)",
		stateUpToDate:                 "up to date",
		stateFastForward:              "remote ahead (pull will fast-forward)",
		stateLocalAhead:               "local ahead (push to publish)",
		stateDiverged:                 "diverged (pull will merge)",
		stateRemoteArchived:           "archived on remote (pull will archive locally)",
		stateRemoteArchivedLocalNewer: "archived on remote, but local is newer (pull keeps yours)",
		stateLocallyArchived:          "archived locally (push will archive remotely)",
		stateArchiveUpToDate:          "archive up to date",
		stateArchiveFastForward:       "remote archive ahead (pull will update)",
		stateArchiveLocalAhead:        "local archive ahead (push to publish)",
		stateArchiveDiverged:          "archived versions diverged (unarchive to reconcile)",
	}
	for _, name := range sorted {
		if only != "" && name != only {
			continue
		}
		rem := s.sideOf("refs/mess-fetched/", "refs/mess-archive-fetched/", "refs/mess-tombstones-fetched/", name)
		loc := s.sideOf("refs/mess/", "refs/mess-archive/", "refs/mess-tombstones/", name)
		reactivate, revive, st := s.classify(rem, loc)
		if st == stateNone {
			continue
		}
		line := preview[st]
		if revive {
			line += " — revives your deleted history"
		}
		if reactivate {
			line += " — unarchives your local copy"
		}
		fmt.Fprintf(out, "%s: %s\n", name, line)
	}
	fmt.Fprintf(out, "(fetched state kept under refs/mess-fetched/ in %s)\n", s.GitDir)
	return nil
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

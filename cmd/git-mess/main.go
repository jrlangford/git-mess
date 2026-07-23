// git-mess — version arbitrary files in hidden object stores, no repo
// required. Thin CLI over internal/mess; all behavior lives there.
package main

import (
	"fmt"
	"os"

	"github.com/jrlangford/git-mess/internal/mess"
)

const usage = `usage: git mess <command> [args]

  init [<dir>]                                    create a local store in <dir> (default .)
  store                                           print which store is in use
  clone <url> [<dir>]                             copy a remote mess and materialize its files
  snapshot <file>... [-n <name>] [-m <msg>] [-f]  record a new version (-f: allow a file
                                                  already tracked by another history)
  snapshot --all [-m <msg>]                       re-snapshot every changed history
  list                                            list all histories
  status [<name>...]                              disk vs last snapshot (all if no name)
  untracked [<dir>]                               files no history tracks (default: store
                                                  root, or cwd for the global store)
  log <name>                                      show a history's versions
  show <name> [<rev>] [<path>]                    print file content at a version
  diff [<name>] [<rev1> [<rev2>]] [--disk]        diff versions (default: last change);
                                                  --disk: vs disk; no name: whole mess
  restore <name>|--all [<rev>]                    write files back to their paths
  move <old-name> <new-name>                      rename a history; if its file is still
                                                  on disk, move the file too
  delete <name> [--prune]                         drop a history (--prune: gc now);
                                                  leaves a tombstone so peers delete too
  push <remote> [<name>]                          publish histories, tombstones, deletions
  pull <remote> [<name>]                          fetch + fast-forward or 3-way merge
  hub-init <path>                                 create a shared fast-forward-only store
`

func fail(err error) {
	fmt.Fprintln(os.Stderr, err)
	os.Exit(1)
}

func usageExit() {
	fmt.Fprint(os.Stderr, usage)
	os.Exit(1)
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}

func main() {
	if len(os.Args) < 2 {
		usageExit()
	}
	cmd, args := os.Args[1], os.Args[2:]
	cwd, err := os.Getwd()
	if err != nil {
		fail(err)
	}
	s := mess.NewStore(cwd)
	out, errOut := os.Stdout, os.Stderr

	switch cmd {
	case "init":
		err = mess.InitLocal(arg(args, 0), out, errOut)
	case "store":
		fmt.Println(s.GitDir)
	case "clone":
		if len(args) < 1 {
			usageExit()
		}
		_, err = mess.Clone(args[0], arg(args, 1), out)
	case "snapshot":
		var files []string
		var o mess.SnapshotOpts
		all := false
		for i := 0; i < len(args); i++ {
			switch args[i] {
			case "-n":
				i++
				o.Name = arg(args, i)
			case "-m":
				i++
				o.Message = arg(args, i)
			case "-f":
				o.Force = true
			case "--all":
				all = true
			default:
				files = append(files, args[i])
			}
		}
		if all {
			err = s.SnapshotAll(o.Message, out)
		} else if len(files) == 0 {
			usageExit()
		} else {
			err = s.Snapshot(files, o, out)
		}
	case "list":
		err = s.List(out)
	case "status":
		err = s.Status(args, out)
	case "untracked":
		err = s.Untracked(arg(args, 0), out)
	case "log":
		if len(args) < 1 {
			usageExit()
		}
		err = s.Log(args[0], out)
	case "show":
		if len(args) < 1 {
			usageExit()
		}
		err = s.Show(args[0], arg(args, 1), arg(args, 2), out)
	case "diff":
		var o mess.DiffOpts
		var rest []string
		for _, a := range args {
			if a == "--disk" || a == "-d" {
				o.Disk = true
			} else {
				rest = append(rest, a)
			}
		}
		o.Name, o.Rev1, o.Rev2 = arg(rest, 0), arg(rest, 1), arg(rest, 2)
		err = s.Diff(o, out)
	case "restore":
		if len(args) < 1 {
			usageExit()
		}
		if args[0] == "--all" {
			err = s.Restore("", "", true, out)
		} else {
			err = s.Restore(args[0], arg(args, 1), false, out)
		}
	case "move", "mv":
		if len(args) < 2 {
			usageExit()
		}
		err = s.Move(args[0], args[1], out)
	case "delete":
		var name string
		prune := false
		for _, a := range args {
			if a == "--prune" {
				prune = true
			} else {
				name = a
			}
		}
		if name == "" {
			usageExit()
		}
		err = s.Delete(name, prune, out)
	case "push":
		if len(args) < 1 {
			usageExit()
		}
		err = s.Push(args[0], arg(args, 1), out, errOut)
	case "pull":
		if len(args) < 1 {
			usageExit()
		}
		err = s.Pull(args[0], arg(args, 1), out)
	case "hub-init":
		if len(args) < 1 {
			usageExit()
		}
		err = mess.HubInit(args[0], out)
	default:
		usageExit()
	}
	if err != nil {
		fail(err)
	}
}

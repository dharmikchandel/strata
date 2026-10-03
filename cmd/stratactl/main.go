// Command stratactl is a small client for a running Strata server: it sends
// logs, searches them, and serves a minimal search web page.
//
//	stratactl ingest -addr 127.0.0.1:7070 < app.log
//	stratactl search -addr 127.0.0.1:7070 -text "timeout error" -tag level=ERROR
//	stratactl ui -server 127.0.0.1:7070 -listen 127.0.0.1:7080
package main

import (
	"fmt"
	"os"
)

const usage = `usage: stratactl <command> [flags]

commands:
  ingest   read log lines from a file or stdin and send them to the server
  search   search stored logs and show how much was skipped
  ui       serve a minimal search web page

run "stratactl <command> -h" for a command's flags.
`

func main() {
	if len(os.Args) < 2 {
		fmt.Fprint(os.Stderr, usage)
		os.Exit(2)
	}
	var err error
	switch os.Args[1] {
	case "ingest":
		err = runIngest(os.Args[2:])
	case "search":
		err = runSearch(os.Args[2:])
	case "ui":
		err = runUI(os.Args[2:])
	case "-h", "-help", "--help", "help":
		fmt.Print(usage)
		return
	default:
		fmt.Fprintf(os.Stderr, "stratactl: unknown command %q\n\n%s", os.Args[1], usage)
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "stratactl:", err)
		os.Exit(1)
	}
}

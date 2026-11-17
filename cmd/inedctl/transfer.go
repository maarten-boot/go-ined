package main

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"

	"github.com/maarten-boot/go-ined/pkg/apitypes"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// Export and import of a network (SPEC-905; Design-network-export.md N10).

// groupExport writes a group of the network view with everything below
// it as JSON, to stdout or -o FILE (mode 0600: notes can be private).
func groupExport(a *app, name string, args []string) error {
	fs := a.flags(name)
	out := fs.String("o", "", "write to this file instead of stdout")
	pos, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	gid, err := parseID(pos[0])
	if err != nil {
		return err
	}
	var raw json.RawMessage
	if err := a.c.Do(a.ctx, http.MethodGet, path("group", gid)+"/export", nil, &raw); err != nil {
		return err
	}
	if *out == "" {
		_, err := fmt.Fprintf(a.out, "%s\n", raw)
		return err
	}
	if err := os.WriteFile(*out, append(raw, '\n'), 0o600); err != nil {
		return usagef("%s: %v", name, err)
	}
	var f apitypes.NetworkExport
	if err := json.Unmarshal(raw, &f); err != nil {
		return err
	}
	g, h := countExport(f.Group)
	fmt.Fprintf(a.out, "exported %s: %s, %s to %s\n", f.From, plural(g, "group"), plural(h, "host"), *out)
	return nil
}

func countExport(g apitypes.ExportGroup) (groups, hosts int) {
	groups, hosts = 1, len(g.Hosts)
	for _, c := range g.Groups {
		cg, ch := countExport(c)
		groups, hosts = groups+cg, hosts+ch
	}
	return groups, hosts
}

// errRefused: the report was printed; the import was (or would be) refused.
type errRefused struct{ n int }

func (e errRefused) Error() string {
	return fmt.Sprintf("refused: %s; nothing was changed (--update updates them)", plural(e.n, "conflict"))
}

// importNetwork: inedctl import FILE|- [--parent ID] [--x X --y Y]
// [--dry-run] [--update]. The file goes to the server as it is.
func (a *app) importNetwork(args []string) error {
	const name = "import"
	fs := a.flags(name)
	parent := fs.Int64("parent", 0, "the group of the network view to import into (default: the top level)")
	x := fs.Float64("x", 0, "the top group's x (with --y)")
	y := fs.Float64("y", 0, "the top group's y (with --x)")
	dry := fs.Bool("dry-run", false, "only report what would happen")
	update := fs.Bool("update", false, "update matching items instead of refusing")
	pos, err := a.parse(fs, args, 1, 1)
	if err != nil {
		return err
	}
	given := set(fs)
	if given["x"] != given["y"] {
		return usagef("%s: give --x and --y together", name)
	}
	if given["parent"] && *parent < 1 {
		return usagef("%s: --parent must be a positive group id", name)
	}
	var data []byte
	if pos[0] == "-" {
		data, err = io.ReadAll(a.stdin)
	} else {
		data, err = os.ReadFile(pos[0])
	}
	if err != nil {
		return usagef("%s: %v", name, err) // not a connection problem (exit 2, not 3)
	}
	if !json.Valid(data) {
		return usagef("%s: %s is not JSON", name, pos[0])
	}
	o := client.ImportOptions{ParentID: *parent, DryRun: *dry, Update: *update}
	if given["x"] {
		o.X, o.Y = x, y
	}
	rep, err := a.c.Import(a.ctx, json.RawMessage(data), o)
	var ae *client.APIError
	refused := errors.As(err, &ae) && ae.Status == http.StatusConflict
	if err != nil && !refused {
		return err
	}
	if a.g.json {
		if refused {
			fmt.Fprintf(a.out, "%s\n", ae.Body)
		} else {
			b, _ := json.MarshalIndent(rep, "", "  ")
			fmt.Fprintf(a.out, "%s\n", b)
		}
	} else {
		a.importReport(rep)
	}
	if n := len(rep.Conflicts) + rep.MoreConflicts; n > 0 && !*update {
		return errRefused{n}
	}
	return nil
}

func (a *app) importReport(r apitypes.ImportReport) {
	for _, l := range r.Lines() {
		fmt.Fprintln(a.out, l)
	}
}

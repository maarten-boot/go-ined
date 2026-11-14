// Command inedctl is the command-line client of ined-server (SPEC-901..903).
//
//	inedctl [--url U] [--token T] [--ca-cert F] [--json] [--config F] <object> <action> [flags] [args]
//
// Run "inedctl -h" for the list of commands.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"net/url"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"

	"github.com/maarten-boot/go-ined/internal/config"
	"github.com/maarten-boot/go-ined/internal/version"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// Exit codes (SPEC-903).
const (
	exitOK    = 0
	exitAPI   = 1   // the API returned an error, or a discovery job failed
	exitUsage = 2   // usage or config error
	exitConn  = 3   // server unreachable, TLS failure, or 401
	exitIntr  = 130 // interrupted (Ctrl-C), except for events
)

const usageText = `Usage: inedctl [global flags] <object> <action> [flags] [args]

Global flags (also accepted after the command):
  --url U        server URL (default from config, INED_URL, http://127.0.0.1:8470)
  --token T      bearer token (default from config or INED_TOKEN)
  --ca-cert F    PEM file with extra trusted CA certificates
  --json         print the API's JSON instead of a table
  --config F     config file (default $XDG_CONFIG_HOME/go-ined/ctl.toml)
  -version       print the version and exit

Commands:
  host list
  host add <ip> [--name N] [--description D] [--parent ID] [--note T] [--unmonitored]
  host show <id>
  host edit <id> [--ip A] [--name N] [--description D] [--monitored=true|false]
  host rm <id>
  host move <id> <group-id|root>              (moves it next to its new parent)
  group list [--view ID]
  group add [--cidr C] [--name N] [--description D] [--parent ID] [--note T] [--expanded] [--view ID]
            [--labels name|ip|both|inherit] [--color #rrggbb|none]
  group show <id>
  group edit <id> [--name N] [--cidr C] [--description D]
  group rm <id> [--reparent | --cascade]
  group move <id> <group-id|root>             (moves it next to its new parent)
  group shift <id> [--dx N] [--dy N]           (moves the group with everything in it)
  group collapse <id>
  group expand <id>
  group labels <id> name|ip|both|inherit      (what the GUI shows under the hosts in it)
  group color <id> '#rrggbb'|none              (the colour of its top bar in the GUI)
  note get|set|clear host|group <id> [text...]   (set reads stdin when no text is given)
  view list
  view add <name> [--order N]
  view show <id>                               (the view and its hosts)
  view rename <id> <name>
  view order <id> <n>
  view rm <id>                                 (its groups and placements go; hosts stay)
  view place <view-id> <host-id> [--parent ID] [--x X --y Y]
                                               (without --parent: the view's root)
  view unplace <view-id> <host-id>
  view arrange <view-id> [--group ID] [--width N] [--height N]
                                               (lay out members on a grid; default 100 x 80)
  discover start <cidr> | --group ID [--parent ID] [--wait]
  discover status <job-id>
  discover cancel <job-id>
  events [--last-id ID]
  health

Exit codes: 0 OK, 1 API error or failed job, 2 usage or config error,
3 connection, TLS or authentication error, 130 interrupted (events: 0).
`

func main() {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	code := run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
	stop()
	os.Exit(code)
}

// usageError is a mistake on the command line (exit code 2).
type usageError struct{ msg string }

func (e usageError) Error() string { return e.msg }

func usagef(format string, a ...any) error { return usageError{fmt.Sprintf(format, a...)} }

// interrupted is returned when the user interrupts a command (exit 130).
type interrupted struct{ msg string }

func (e interrupted) Error() string { return e.msg }

// errJob is returned when a waited-for discovery job fails or is cancelled.
var errJob = errors.New("discovery job did not finish")

// errHelp: -h was given; the usage was printed.
var errHelp = errors.New("help")

type globals struct {
	url, token, caCert, config string
	json, version              bool
	given                      map[string]bool // global flags given on the command line
}

// register adds the global flags to fs, keeping values already parsed.
func (g *globals) register(fs *flag.FlagSet) {
	fs.StringVar(&g.url, "url", g.url, "server URL")
	fs.StringVar(&g.token, "token", g.token, "bearer token")
	fs.StringVar(&g.caCert, "ca-cert", g.caCert, "PEM file with extra trusted CA certificates")
	fs.BoolVar(&g.json, "json", g.json, "print JSON")
	fs.StringVar(&g.config, "config", g.config, "config file")
}

type app struct {
	ctx    context.Context
	g      globals
	stdin  io.Reader
	out    io.Writer
	errOut io.Writer
	c      *client.Client
}

func run(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	a := &app{ctx: ctx, stdin: stdin, out: stdout, errOut: stderr}
	a.g.given = map[string]bool{}
	err := a.main(args)
	var ie interrupted
	if err != nil && ctx.Err() != nil && !errors.As(err, &ie) {
		err = interrupted{"interrupted"}
	}
	code := exitCode(err)
	var ue usageError
	switch {
	case err == nil, errors.Is(err, errHelp):
	case errors.As(err, &ue):
		fmt.Fprintf(stderr, "inedctl: %v\nRun 'inedctl -h' for usage.\n", err)
	case errors.Is(err, errJob):
		// The job's final state was already printed.
	default:
		fmt.Fprintf(stderr, "inedctl: %v\n", err)
	}
	return code
}

func exitCode(err error) int {
	var (
		ie  interrupted
		ue  usageError
		ae  *client.APIError
		ce  configError
		ue2 *url.Error
		ne  net.Error
	)
	switch {
	case err == nil, errors.Is(err, errHelp):
		return exitOK
	case errors.As(err, &ie):
		return exitIntr
	case errors.As(err, &ue), errors.As(err, &ce):
		return exitUsage
	case errors.As(err, &ae):
		if ae.Status == 401 {
			return exitConn
		}
		return exitAPI
	case errors.As(err, &ue2), errors.As(err, &ne):
		return exitConn
	default:
		return exitAPI
	}
}

// configError is a problem with the config file or the server options.
type configError struct{ err error }

func (e configError) Error() string { return e.err.Error() }
func (e configError) Unwrap() error { return e.err }

func (a *app) main(args []string) error {
	root := flag.NewFlagSet("inedctl", flag.ContinueOnError)
	root.SetOutput(io.Discard)
	a.g.register(root)
	root.BoolVar(&a.g.version, "version", false, "print the version")
	if err := root.Parse(args); err != nil {
		return a.flagErr(err)
	}
	a.g.visit(root)
	if a.g.version {
		fmt.Fprintln(a.out, "inedctl", version.Version)
		return nil
	}
	rest := root.Args()
	if len(rest) == 0 {
		return usagef("missing command")
	}
	obj, rest := rest[0], rest[1:]
	switch obj {
	case "health":
		return a.health(rest)
	case "events":
		return a.events(rest)
	}
	actions, ok := commands[obj]
	if !ok {
		return usagef("unknown command %q", obj)
	}
	if len(rest) == 0 {
		return usagef("%s: missing action", obj)
	}
	fn, ok := actions[rest[0]]
	if !ok {
		return usagef("%s: unknown action %q", obj, rest[0])
	}
	return fn(a, obj+" "+rest[0], rest[1:])
}

func (a *app) flagErr(err error) error {
	if errors.Is(err, flag.ErrHelp) {
		io.WriteString(a.out, usageText)
		return errHelp
	}
	return usageError{err.Error()}
}

// visit records which global flags fs got, so even an empty value
// (--token "") overrides the config file and the environment.
func (g *globals) visit(fs *flag.FlagSet) {
	fs.Visit(func(f *flag.Flag) {
		switch f.Name {
		case "url", "token", "ca-cert", "config", "json":
			g.given[f.Name] = true
		}
	})
}

// flags returns a FlagSet for one command, with the global flags added.
func (a *app) flags(name string) *flag.FlagSet {
	fs := flag.NewFlagSet(name, flag.ContinueOnError)
	fs.SetOutput(io.Discard)
	a.g.register(fs)
	return fs
}

// parse parses flags and positional arguments in any order and checks the
// number of positional arguments (max < 0: no limit). It then connects.
func (a *app) parse(fs *flag.FlagSet, args []string, minArgs, maxArgs int) ([]string, error) {
	var pos []string
	for {
		if err := fs.Parse(args); err != nil {
			return nil, a.flagErr(err)
		}
		a.g.visit(fs)
		if fs.NArg() == 0 {
			break
		}
		// "--" ends the flags: everything after it is positional.
		if n := len(args) - fs.NArg(); n > 0 && args[n-1] == "--" {
			pos = append(pos, fs.Args()...)
			break
		}
		pos = append(pos, fs.Arg(0))
		args = fs.Args()[1:]
	}
	switch {
	case len(pos) < minArgs:
		return nil, usagef("%s: missing argument", fs.Name())
	case maxArgs >= 0 && len(pos) > maxArgs:
		return nil, usagef("%s: too many arguments", fs.Name())
	}
	return pos, a.connect()
}

// connect builds the client from the config file, the environment and the
// flags (SPEC-805).
func (a *app) connect() error {
	cfg, err := config.LoadClient(a.g.config)
	if err != nil {
		return configError{err}
	}
	s := cfg.Server
	if a.g.given["url"] {
		s.URL = a.g.url
	}
	if a.g.given["token"] {
		s.Token = a.g.token
	}
	if a.g.given["ca-cert"] {
		s.CACert = a.g.caCert
	}
	c, err := client.New(client.Options{URL: s.URL, Token: s.Token, CACert: s.CACert})
	if err != nil {
		return configError{err}
	}
	if c.TokenInClear() {
		fmt.Fprintf(a.errOut, "inedctl: warning: sending the token unencrypted to %s; use https\n", c.URL())
	}
	a.c = c
	return nil
}

func parseID(s string) (int64, error) {
	id, err := strconv.ParseInt(s, 10, 64)
	if err != nil || id < 1 {
		return 0, usagef("invalid id %q: want a positive integer", s)
	}
	return id, nil
}

// parseParent parses "<group-id>" or "root".
func parseParent(s string) (*int64, error) {
	if strings.EqualFold(s, "root") {
		return nil, nil
	}
	id, err := parseID(s)
	if err != nil {
		return nil, usagef("invalid group %q: want a group id or \"root\"", s)
	}
	return &id, nil
}

// set reports which flags were given on the command line.
func set(fs *flag.FlagSet) map[string]bool {
	m := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { m[f.Name] = true })
	return m
}

// call sends one request. With --json it prints the response body
// unchanged (SPEC-902); otherwise it decodes into out and calls show.
func (a *app) call(method, path string, body, out any, show func()) error {
	if a.g.json {
		var raw json.RawMessage
		err := a.c.Do(a.ctx, method, path, body, &raw)
		if err == nil && len(raw) > 0 {
			fmt.Fprintf(a.out, "%s\n", raw)
		}
		return err
	}
	if err := a.c.Do(a.ctx, method, path, body, out); err != nil {
		return err
	}
	if show != nil {
		show()
	}
	return nil
}

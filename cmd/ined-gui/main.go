// Command ined-gui is the desktop client of ined-server (SPEC-701..718).
//
//	ined-gui [-url U] [-token T] [-ca-cert F] [-config F] [-version]
//
// Settings come from the flags, then INED_URL / INED_TOKEN, then gui.toml,
// then the defaults (SPEC-805).
package main

import (
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"fyne.io/fyne/v2/app"

	"github.com/maarten-boot/go-ined/internal/config"
	"github.com/maarten-boot/go-ined/internal/gui"
	"github.com/maarten-boot/go-ined/internal/theme"
	"github.com/maarten-boot/go-ined/internal/version"
	"github.com/maarten-boot/go-ined/pkg/client"
)

// appID identifies the app to Fyne (preferences, desktop integration).
const appID = "io.github.maarten-boot.go-ined"

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

// settings is everything ined-gui needs to start.
type settings struct {
	client     client.Options
	contrast   theme.Contrast
	wrapNames  bool
	configPath string // where contrast changes are saved
	warnings   []string
	version    bool
}

// resolve parses the flags and reads the config (SPEC-701, 805). A bad
// contrast setting falls back to AA with a warning (TC-006).
func resolve(args []string, stderr io.Writer) (settings, error) {
	var s settings
	fs := flag.NewFlagSet("ined-gui", flag.ContinueOnError)
	fs.SetOutput(stderr)
	url := fs.String("url", "", "server URL (default from config, INED_URL, http://127.0.0.1:8470)")
	token := fs.String("token", "", "bearer token (default from config or INED_TOKEN)")
	caCert := fs.String("ca-cert", "", "PEM file with extra trusted CA certificates")
	cfgPath := fs.String("config", "", "config file (default $XDG_CONFIG_HOME/go-ined/gui.toml)")
	fs.BoolVar(&s.version, "version", false, "print the version and exit")
	if err := fs.Parse(args); err != nil {
		return s, err
	}
	if fs.NArg() > 0 {
		return s, fmt.Errorf("unexpected argument %q", fs.Arg(0))
	}
	if s.version {
		return s, nil
	}
	cfg, err := config.LoadGUI(*cfgPath)
	if err != nil {
		return s, err
	}
	given := map[string]bool{}
	fs.Visit(func(f *flag.Flag) { given[f.Name] = true })
	srv := cfg.Server
	if given["url"] {
		srv.URL = *url
	}
	if given["token"] {
		srv.Token = *token
	}
	if given["ca-cert"] {
		srv.CACert = *caCert
	}
	s.client = client.Options{URL: srv.URL, Token: srv.Token, CACert: srv.CACert}
	s.wrapNames = cfg.Map.WrapNames
	s.contrast, err = theme.Parse(cfg.Contrast.Preset, cfg.Contrast.MinRatio)
	if err != nil {
		s.warnings = append(s.warnings, fmt.Sprintf("%v; using %s", err, s.contrast))
	}
	if s.configPath, err = config.GUIPath(*cfgPath); err != nil {
		s.warnings = append(s.warnings, "contrast changes can't be saved: "+err.Error())
	}
	return s, nil
}

func run(args []string, stdout, stderr io.Writer) int {
	s, err := resolve(args, stderr)
	switch {
	case errors.Is(err, flag.ErrHelp):
		return 0
	case err != nil:
		fmt.Fprintln(stderr, "ined-gui:", err)
		return 2
	case s.version:
		fmt.Fprintln(stdout, "ined-gui", version.Version)
		return 0
	}
	c, err := client.New(s.client)
	if err != nil {
		fmt.Fprintln(stderr, "ined-gui:", err)
		return 2
	}
	for _, w := range s.warnings {
		fmt.Fprintln(stderr, "ined-gui: warning:", w)
	}
	if c.TokenInClear() {
		fmt.Fprintf(stderr, "ined-gui: warning: sending the token unencrypted to %s; use https\n", c.URL())
	}
	var save func(theme.Contrast) error
	var saveWrap func(bool) error
	if s.configPath != "" {
		path := s.configPath
		save = func(ct theme.Contrast) error { return config.SaveContrast(path, ct.Preset, ct.Min) }
		saveWrap = func(on bool) error { return config.SaveWrapNames(path, on) }
	}
	g := gui.New(app.NewWithID(appID), gui.Options{
		Client: c, Contrast: s.contrast, SaveContrast: save, Warnings: s.warnings,
		WrapNames: s.wrapNames, SaveWrapNames: saveWrap,
		Title: "go-ined — " + c.URL(),
	})
	g.ShowAndRun()
	return 0
}

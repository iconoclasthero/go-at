package main

// Settings come from, highest priority first:
//
//	1. command-line flags (where one exists, e.g. -s SHELL)
//	2. environment variables (AT_SPOOL, AT_SHELL, ...)
//	3. the config file
//	4. built-in defaults
//
// The config file is the first of:
//
//	--config FILE            (must exist)
//	$AT_CONFIG               (must exist)
//	$XDG_CONFIG_HOME/go-at.conf, else ~/.config/go-at.conf   (optional)
//
// Format: "key = value" per line; "#" starts a comment; values may be quoted;
// a leading "~/" means your home directory. Unknown keys and bad values are
// errors (with the line number) so typos are never silently ignored.

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

type settingDef struct {
	key, env, def, desc string
	check               func(string) error
}

func checkBool(v string) error {
	if _, ok := parseBool(v); !ok {
		return fmt.Errorf("%q is not yes/no (also true/false, on/off, 1/0)", v)
	}
	return nil
}

func parseBool(v string) (val, ok bool) {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "yes", "true", "on", "y":
		return true, true
	case "0", "no", "false", "off", "n":
		return false, true
	}
	return false, false
}

var settingDefs = []settingDef{
	{"spool", "AT_SPOOL", "$XDG_STATE_HOME/at, else ~/.local/state/at", "where jobs, logs and history live", nil},
	{"shell", "AT_SHELL", "bash", "shell that runs jobs", nil},
	{"batch_load", "AT_BATCH_LOAD", "1.5", "load average below which --batch jobs start (0 holds them)", func(v string) error {
		if f, err := strconv.ParseFloat(v, 64); err != nil || f < 0 {
			return fmt.Errorf("%q is not a number >= 0", v)
		}
		return nil
	}},
	{"keep_days", "AT_KEEP_DAYS", "30", "days to keep records, logs and saved commands of ended jobs", func(v string) error {
		if n, err := strconv.Atoi(v); err != nil || n < 0 {
			return fmt.Errorf("%q is not a whole number >= 0", v)
		}
		return nil
	}},
	{"autostart", "AT_AUTOSTART", "no", "go-at starts go-atd when none is running", checkBool},
	{"keep_commands", "AT_KEEP_COMMANDS", "yes", "save the commands of ended jobs (go-at -c works on them)", checkBool},
}

var (
	confPath   string                // file in use, "" if none
	confValues = map[string]string{} // env name -> value from the file
)

// cfg returns a setting: the environment variable if set (non-empty), else the
// config file's value, else "".
func cfg(envName string) string {
	if v := os.Getenv(envName); v != "" {
		return v
	}
	return confValues[envName]
}

// cfgBool is cfg as a yes/no with a default.
func cfgBool(envName string, def bool) bool {
	if b, ok := parseBool(cfg(envName)); ok {
		return b
	}
	return def
}

func expandHome(p string) string {
	if p == "~" || strings.HasPrefix(p, "~/") {
		if h, err := os.UserHomeDir(); err == nil {
			return filepath.Join(h, strings.TrimPrefix(p, "~"))
		}
	}
	return p
}

// extractConfigFlag removes --config FILE / --config=FILE from args.
func extractConfigFlag(args []string) (rest []string, path string, err error) {
	for i := 0; i < len(args); i++ {
		a := args[i]
		if a == "--" {
			rest = append(rest, args[i:]...)
			break
		}
		switch {
		case a == "--config" || a == "-config":
			if i+1 >= len(args) {
				return nil, "", fmt.Errorf("--config needs a file name")
			}
			path = args[i+1]
			i++
		case strings.HasPrefix(a, "--config="):
			path = strings.TrimPrefix(a, "--config=")
		default:
			rest = append(rest, a)
		}
	}
	if path == "" && len(rest) != len(args) {
		return nil, "", fmt.Errorf("--config needs a file name")
	}
	return rest, path, nil
}

// loadConfig reads the config file chosen by flag, $AT_CONFIG or the default
// location. A file named explicitly must exist; the default may be absent.
func loadConfig(flagPath string) error {
	path, explicit := flagPath, flagPath != ""
	if path == "" {
		if env := os.Getenv("AT_CONFIG"); env != "" {
			path, explicit = env, true
		}
	}
	if path == "" {
		base := os.Getenv("XDG_CONFIG_HOME")
		if base == "" || !filepath.IsAbs(base) {
			h, err := os.UserHomeDir()
			if err != nil {
				return nil // no home: no default config
			}
			base = filepath.Join(h, ".config")
		}
		path = filepath.Join(base, "go-at.conf")
	}
	path = expandHome(path)
	f, err := os.Open(path)
	if err != nil {
		if !explicit && os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("config file: %v", err)
	}
	defer f.Close()
	confPath = path
	byKey := map[string]settingDef{}
	for _, d := range settingDefs {
		byKey[d.key] = d
	}
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		k, v, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s line %d: expected key = value", path, n)
		}
		k, v = strings.ToLower(strings.TrimSpace(k)), strings.TrimSpace(v)
		if len(v) >= 2 && (v[0] == '"' || v[0] == '\'') && v[len(v)-1] == v[0] {
			v = v[1 : len(v)-1]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		d, known := byKey[k]
		if !known {
			names := make([]string, 0, len(settingDefs))
			for _, d := range settingDefs {
				names = append(names, d.key)
			}
			return fmt.Errorf("%s line %d: unknown setting %q (known: %s)", path, n, k, strings.Join(names, ", "))
		}
		if k == "spool" {
			v = expandHome(v)
		}
		if v == "" {
			return fmt.Errorf("%s line %d: %s has no value", path, n, k)
		}
		if d.check != nil {
			if err := d.check(v); err != nil {
				return fmt.Errorf("%s line %d: %s: %v", path, n, k, err)
			}
		}
		confValues[d.env] = v
	}
	return sc.Err()
}

// showConfig prints every setting, its effective value and where it came from.
func showConfig() {
	if confPath != "" {
		fmt.Printf("config file: %s\n", confPath)
	} else {
		fmt.Println("config file: none (default location: ~/.config/go-at.conf or $XDG_CONFIG_HOME/go-at.conf)")
	}
	for _, d := range settingDefs {
		val, src := d.def, "default"
		if v := os.Getenv(d.env); v != "" {
			val, src = v, "environment "+d.env
		} else if v, ok := confValues[d.env]; ok {
			val, src = v, "config file"
		}
		if d.key == "spool" && src == "default" {
			if sp, err := OpenSpool(); err == nil {
				val = sp.Dir
			}
		}
		fmt.Printf("%-14s = %-28s (%s)\n", d.key, val, src)
	}
}

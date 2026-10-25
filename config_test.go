package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeConf(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "my.conf")
	if err := os.WriteFile(p, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestConfigPrecedence(t *testing.T) {
	e := newEnv(t)
	cfgSpool := filepath.Join(t.TempDir(), "from-config")
	envSpool := filepath.Join(t.TempDir(), "from-env")
	conf := writeConf(t, "# comment\nspool = "+cfgSpool+"\nbatch_load = \"2.5\"\nkeep_days=7   # trailing\nautostart = no\n")

	// no AT_SPOOL: the config file's value is used
	e.extra = []string{"AT_SPOOL=", "AT_CONFIG=" + conf}
	out, se, code := e.run("", "at", "--show-config")
	if code != 0 || !strings.Contains(out, cfgSpool+" ") && !strings.Contains(out, cfgSpool) ||
		!strings.Contains(out, "(config file)") || !strings.Contains(out, "2.5") || !strings.Contains(out, "keep_days      = 7") {
		t.Fatalf("config values not shown: %d\n%s%s", code, out, se)
	}
	// the environment beats the file
	e.extra = []string{"AT_SPOOL=" + envSpool, "AT_CONFIG=" + conf, "AT_BATCH_LOAD=9"}
	out, _, _ = e.run("", "at", "--show-config")
	if !strings.Contains(out, envSpool) || !strings.Contains(out, "environment AT_SPOOL") ||
		!strings.Contains(out, "environment AT_BATCH_LOAD") || !strings.Contains(out, "keep_days      = 7") {
		t.Errorf("environment should win over the file:\n%s", out)
	}
	// --config beats $AT_CONFIG
	other := writeConf(t, "keep_days = 3\n")
	e.extra = []string{"AT_CONFIG=" + conf}
	out, _, _ = e.run("", "at", "--config", other, "--show-config")
	if !strings.Contains(out, "keep_days      = 3") || !strings.Contains(out, other) {
		t.Errorf("--config should beat AT_CONFIG:\n%s", out)
	}
	out, _, _ = e.run("", "at", "--config="+other, "--show-config")
	if !strings.Contains(out, "keep_days      = 3") {
		t.Errorf("--config=FILE form:\n%s", out)
	}
	// jobs really land in the configured spool
	e.extra = []string{"AT_SPOOL=", "AT_CONFIG=" + conf}
	e.run("true\n", "at", "-n", "viaconf", "now", "+", "1", "hour")
	if ents, _ := os.ReadDir(filepath.Join(cfgSpool, "jobs")); len(ents) != 1 {
		t.Errorf("job should be in the configured spool: %v", ents)
	}
	// works for every command: atq reads the same file
	if out, _, _ := e.run("", "atq"); !strings.Contains(out, "viaconf") {
		t.Errorf("atq should use the configured spool:\n%s", out)
	}
}

func TestDefaultConfigLocation(t *testing.T) {
	e := newEnv(t)
	dir := t.TempDir()
	os.WriteFile(filepath.Join(dir, "go-at.conf"), []byte("keep_days = 11\n"), 0o600)
	e.extra = []string{"XDG_CONFIG_HOME=" + dir}
	if out, _, _ := e.run("", "at", "--show-config"); !strings.Contains(out, "keep_days      = 11") {
		t.Errorf("$XDG_CONFIG_HOME/go-at.conf not read:\n%s", out)
	}
	// no XDG_CONFIG_HOME: ~/.config/go-at.conf
	home := t.TempDir()
	os.MkdirAll(filepath.Join(home, ".config"), 0o700)
	os.WriteFile(filepath.Join(home, ".config", "go-at.conf"), []byte("keep_days = 12\n"), 0o600)
	e.extra = []string{"XDG_CONFIG_HOME=", "HOME=" + home}
	if out, _, _ := e.run("", "at", "--show-config"); !strings.Contains(out, "keep_days      = 12") {
		t.Errorf("~/.config/go-at.conf not read:\n%s", out)
	}
	// an absent default file is fine
	e.extra = []string{"XDG_CONFIG_HOME=" + t.TempDir()}
	if _, se, code := e.run("", "at", "--show-config"); code != 0 {
		t.Errorf("absent default config must not be an error: %s", se)
	}
}

func TestConfigErrors(t *testing.T) {
	e := newEnv(t)
	cases := []struct{ body, want string }{
		{"spol = /x\n", `line 1: unknown setting "spol"`},
		{"keep_days = lots\n", "line 1: keep_days"},
		{"batch_load = -1\n", "line 1: batch_load"},
		{"autostart = maybe\n", "line 1: autostart"},
		{"\nno equals sign\n", "line 2: expected key = value"},
		{"shell =\n", "line 1: shell has no value"},
	}
	for _, c := range cases {
		_, se, code := e.run("", "atq", "--config", writeConf(t, c.body))
		if code == 0 || !strings.Contains(se, c.want) {
			t.Errorf("%q: code %d, stderr %q (want %q)", c.body, code, se, c.want)
		}
	}
	// an explicitly named file must exist, via flag or variable
	if _, se, code := e.run("", "atq", "--config", "/no/such/file"); code == 0 || !strings.Contains(se, "config file") {
		t.Errorf("missing --config file: %d %q", code, se)
	}
	e.extra = []string{"AT_CONFIG=/no/such/file"}
	if _, _, code := e.run("", "atq"); code == 0 {
		t.Errorf("missing AT_CONFIG file should fail")
	}
	if _, _, code := e.run("", "atq", "--config"); code == 0 {
		t.Errorf("--config without a value should fail")
	}
	// help never depends on a valid config
	e.extra = []string{"AT_CONFIG=/no/such/file"}
	if _, _, code := e.run("", "at", "--help"); code != 0 {
		t.Errorf("--help must work with a broken config")
	}
}

func TestSavedCommands(t *testing.T) {
	e := newEnv(t)
	e.extra = []string{"SECRET_TOKEN=hunter2"}
	e.submit("echo hello-job\nexit 3\n", "-n", "kept", "now")
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "job failed", 15e9, func() bool { j, ok := e.job("kept", true); return ok && j.State == stFailed })
	out, se, code := e.run("", "at", "-c", "kept")
	if code != 0 || out != "echo hello-job\nexit 3\n" {
		t.Fatalf("-c on an ended job: %d %q %q", code, out, se)
	}
	if strings.Contains(out, "hunter2") {
		t.Errorf("environment leaked into saved commands")
	}
	b, _ := os.ReadFile(filepath.Join(e.spool, "history", "1.sh"))
	if strings.Contains(string(b), "SECRET_TOKEN") {
		t.Errorf("history file must hold no environment: %q", b)
	}
	if fi, err := os.Stat(filepath.Join(e.spool, "history", "1.sh")); err != nil || fi.Mode().Perm() != 0o600 {
		t.Errorf("saved commands should be 0600: %v %v", fi, err)
	}
}

func TestKeepCommandsOff(t *testing.T) {
	e := newEnv(t)
	conf := writeConf(t, "keep_commands = no\n")
	e.extra = []string{"AT_CONFIG=" + conf}
	e.submit("true\n", "-n", "gone", "now")
	stop, _ := e.startDaemon()
	defer stop()
	waitFor(t, "job finished", 15e9, func() bool { j, ok := e.job("gone", true); return ok && j.State == stFinished })
	if _, se, code := e.run("", "at", "-c", "gone"); code == 0 || !strings.Contains(se, "not kept") {
		t.Errorf("-c should explain commands were not kept: %d %q", code, se)
	}
	if _, err := os.Stat(filepath.Join(e.spool, "history", "1.sh")); err == nil {
		t.Errorf("nothing should be saved when keep_commands = no")
	}
}

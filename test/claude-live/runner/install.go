package runner

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/BurntSushi/toml"
)

func (r *Run) Preflight(ctx context.Context) error {
	if err := validateHome(r.Home, filepath.Join(r.scopeRoot(), "home")); err != nil {
		return err
	}
	for _, name := range []string{"claude", "tmux", "sqlite3", "bash", "cp", "jq"} {
		if _, err := exec.LookPath(name); err != nil {
			return fmt.Errorf("missing prerequisite %s", name)
		}
	}
	// Auth status may contain account details. Retain only login/subscription
	// booleans, never stdout, credential files, environment or account identity.
	var out, errout limitedBuffer
	ctx, c := context.WithTimeout(ctx, 15*time.Second)
	defer c()
	started := r.Now().UTC()
	code, err := r.Commands.Execute(ctx, []string{"claude", "auth", "status", "--json"}, r.Commands.Environment, &out, &errout)
	var auth struct {
		LoggedIn         bool   `json:"loggedIn"`
		SubscriptionType string `json:"subscriptionType"`
	}
	valid := err == nil && code == 0 && json.Unmarshal(out.b, &auth) == nil
	r.Commands.mu.Lock()
	r.Commands.rows = append(r.Commands.rows, Command{Name: "claude-auth-status-redacted", Argv: []string{"claude", "auth", "status", "--json"}, Started: started, Ended: r.Now().UTC(), ExitCode: code, Timeout: ctx.Err() != nil, Stdout: "raw/auth-status.json"})
	r.Commands.mu.Unlock()
	if err = r.Evidence.JSON("raw/auth-status.json", map[string]bool{"logged_in": auth.LoggedIn, "subscription": auth.SubscriptionType != ""}); err != nil {
		return err
	}
	if !valid {
		return errors.New("Claude authentication status unavailable; intelligence required")
	}
	if !auth.LoggedIn || auth.SubscriptionType == "" {
		return errors.New("Claude subscription login unavailable; stop for intelligence")
	}
	cmd, v, err := r.Commands.Run(ctx, "claude-version", 10*time.Second, "claude", "--version")
	if err != nil {
		return err
	}
	r.Manifest.ProviderVersion = strings.TrimSpace(string(v))
	r.ProviderCommand = cmd.Stdout
	return nil
}

// Install requires an explicit maintenance assertion. It refuses live or
// uncheckable recorded agents, retains a consistent DB backup and the original
// DB files, and never repairs legacy links or automatically restores state.
func (r *Run) Install(ctx context.Context, backup string, recreateV21, maintenance bool) error {
	if !maintenance {
		return errors.New("install requires --maintenance-window after excluding all old writers")
	}
	if err := r.Preflight(ctx); err != nil {
		return err
	}
	if _, err := r.VerifyCandidate(ctx, false); err != nil {
		return err
	}
	if filepath.Clean(backup) != backup || filepath.Dir(backup) != r.scopeRoot() || backup == r.Evidence.Root {
		return errors.New("backup must be a new private test-root directory outside evidence")
	}
	if strings.ContainsAny(backup, "'\n\r\t") {
		return errors.New("unsafe backup path")
	}
	if _, err := Confined(r.scopeRoot(), filepath.Base(backup)); err != nil {
		return err
	}
	cfg, err := Confined(r.Home, ".config/agent-director")
	if err != nil {
		return err
	}
	// Reject relocated stores rather than guessing around their recovery set.
	config, err := os.ReadFile(filepath.Join(cfg, "config.toml"))
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	dbPath := filepath.Join(cfg, "state.db")
	if _, err = Confined(cfg, "state.db"); err != nil {
		return err
	}
	var settings struct {
		Store struct {
			DBPath string `toml:"db_path"`
		} `toml:"store"`
	}
	if _, err = toml.Decode(string(config), &settings); err != nil {
		return errors.New("invalid install config")
	}
	if p := settings.Store.DBPath; p != "" && p != dbPath && p != "~/.config/agent-director/state.db" {
		return errors.New("custom db_path requires an operator install")
	}
	var version int
	if _, err = os.Stat(dbPath); err == nil {
		db, err := OpenReadOnly(dbPath)
		if err != nil {
			return err
		}
		defer db.Close()
		if err = db.QueryRowContext(ctx, "PRAGMA user_version").Scan(&version); err != nil {
			return err
		}
		rows, err := db.QueryContext(ctx, "SELECT state,pid,proc_starttime FROM spawns")
		if err != nil {
			return err
		}
		for rows.Next() {
			var state string
			var pid any
			var start any
			if err = rows.Scan(&state, &pid, &start); err != nil {
				rows.Close()
				return err
			}
			if state != "ended" && state != "missing" {
				rows.Close()
				return errors.New("old agent still live; operator must stop it")
			}
			if pid != nil {
				p, ok := pid.(int64)
				s, _ := start.(string)
				gone, e := ProcessGone(int(p), s)
				if !ok || e != nil || !gone {
					rows.Close()
					return errors.New("old process exit not proven")
				}
			}
		}
		err = errors.Join(rows.Err(), rows.Close())
		if err != nil {
			return err
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if recreateV21 && version != 21 {
		return errors.New("--recreate-v21 requires an existing v21 store")
	}
	if err = os.Mkdir(backup, 0700); err != nil {
		return err
	}
	if _, err = os.Stat(cfg); err == nil {
		if _, _, err = r.Commands.Run(ctx, "backup-install", time.Minute, "cp", "-a", "--", cfg, filepath.Join(backup, "install")); err != nil {
			return err
		}
	}
	if version > 0 {
		_, _, err = r.Commands.Run(ctx, "backup-store", 30*time.Second, "sqlite3", "-readonly", "-batch", "-init", "/dev/null", dbPath, ".backup '"+filepath.Join(backup, "state.db")+"'")
		if err != nil {
			return err
		}
	}
	if err = r.Evidence.JSON("raw/backup.json", map[string]any{"path": backup, "schema": version, "recreated": recreateV21}); err != nil {
		return err
	}
	if recreateV21 {
		moved := filepath.Join(backup, "original-store")
		if err = os.Mkdir(moved, 0700); err != nil {
			return err
		}
		for _, name := range []string{"state.db", "state.db-wal", "state.db-shm", "state.db.initlock"} {
			p := filepath.Join(cfg, name)
			if _, err = os.Lstat(p); os.IsNotExist(err) {
				continue
			}
			if err != nil {
				return err
			}
			if err = os.Rename(p, filepath.Join(moved, name)); err != nil {
				return err
			}
		}
	}
	// Exercise the installer's --no-hooks preservation as well as the later
	// probe's spawn-time hook merge. Cleanup verifies that every exact entry
	// survives installation before removing this install phase's recorder.
	if err = r.StartRecorder([]string{"SessionStart", "FileChanged", "PostToolBatch", "UserPromptSubmit", "Stop"}); err != nil {
		return err
	}
	_, _, err = r.Commands.Run(ctx, "install-candidate", 4*time.Minute, "bash", filepath.Join(r.Kit, "skills/install-agent-director/install.sh"), "--binary", filepath.Join(r.Kit, "payload/agent-director-linux-"+runtime.GOARCH), "--admin-binary", filepath.Join(r.Kit, "payload/agent-director-admin-linux-"+runtime.GOARCH), "--no-hooks", "--keep-prior", "--symlink-dir", filepath.Join(r.Home, ".local/bin"))
	if err != nil {
		r.Problem("candidate-install", "fail", err)
		return err
	}
	_, err = r.VerifyCandidate(ctx, true)
	if err != nil {
		r.Problem("installed-candidate", "fail", err)
	}
	return err
}

func (r *Run) StartRecorder(events []string) error {
	exe := filepath.Join(r.Kit, "bin/claude-live")
	b, err := os.ReadFile(exe)
	if err != nil {
		return err
	}
	if err = os.MkdirAll(filepath.Join(r.Evidence.Root, "raw/hooks"), 0700); err != nil {
		return err
	}
	r.Manifest.RecordingHook = RecordingHook{Command: exe, SHA256: Digest(b), Events: events, Directory: "raw/hooks"}
	r.Settings, err = installRecorderAt(r.scopeRoot(), r.Home, exe, r.Evidence.Root, r.Manifest.RunID, events)
	if err == nil {
		r.Manifest.RecordingHook.Installed = true
	}
	return err
}

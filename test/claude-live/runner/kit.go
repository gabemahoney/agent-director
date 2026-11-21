package runner

import (
	"context"
	"debug/buildinfo"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"time"
)

type Kit struct {
	Schema       int                 `json:"schema"`
	BaseCommit   string              `json:"base_commit"`
	SnapshotTree string              `json:"snapshot_tree"`
	SourceSHA256 string              `json:"source_sha256"`
	DiffSHA256   string              `json:"diff_sha256"`
	Dirty        bool                `json:"dirty"`
	Version      string              `json:"version"`
	CommitStamp  string              `json:"commit_stamp"`
	Platform     string              `json:"platform"`
	Files        map[string]Artifact `json:"files"`
}

// CandidateEvidence binds the manifest to retained kit and successful command
// facts. It is replayed offline without executing the kit or reading HOME.
type CandidateEvidence struct {
	Schema          int    `json:"schema"`
	RunID           string `json:"run_id"`
	Kit             Kit    `json:"kit"`
	KitSHA256       string `json:"kit_sha256"`
	Installed       bool   `json:"installed"`
	ProviderVersion string `json:"provider_version"`
	ProviderCommand string `json:"provider_command"`
	RuntimeCommand  string `json:"runtime_command"`
	AdminCommand    string `json:"admin_command"`
}

func VerifyKit(root string) (Kit, error) {
	var k Kit
	b, err := Read(root, "kit.json")
	if err != nil {
		return k, err
	}
	if err = json.Unmarshal(b, &k); err != nil {
		return k, err
	}
	if k.Schema != Schema || len(k.BaseCommit) != 40 || len(k.SnapshotTree) != 40 || len(k.SourceSHA256) != 64 || len(k.DiffSHA256) != 64 || k.Version == "" || k.CommitStamp != k.BaseCommit || len(k.Files) < 4 {
		return k, errors.New("incomplete kit provenance")
	}
	if k.Platform != runtime.GOOS+"/"+runtime.GOARCH || runtime.GOOS != "linux" {
		return k, errors.New("kit requires its native Linux target")
	}
	for _, name := range []string{"bin/claude-live", "payload/agent-director-linux-" + runtime.GOARCH, "payload/agent-director-admin-linux-" + runtime.GOARCH, "skills/install-agent-director/install.sh"} {
		if _, ok := k.Files[name]; !ok {
			return k, fmt.Errorf("kit missing %s", name)
		}
	}
	for _, name := range []string{"agent-director.cron", "agent-director-find-missing.service", "agent-director-find-missing.timer", "agent-director-expire.service", "agent-director-expire.timer"} {
		if _, ok := k.Files["skills/install-agent-director/schedule/"+name]; !ok {
			return k, fmt.Errorf("kit missing installer asset %s", name)
		}
	}
	for name, a := range k.Files {
		b, err := Read(root, name)
		if err != nil {
			return k, err
		}
		if Digest(b) != a.SHA256 || int64(len(b)) != a.Bytes {
			return k, fmt.Errorf("kit hash mismatch: %s", name)
		}
	}
	for _, component := range []string{"agent-director", "agent-director-admin", "claude-live"} {
		name := "payload/" + component + "-linux-" + runtime.GOARCH
		wantPath := "github.com/gabemahoney/agent-director/cmd/" + component
		if component == "claude-live" {
			name = "bin/claude-live"
			wantPath = "github.com/gabemahoney/agent-director/test/claude-live/cmd/claude-live"
		}
		info, e := buildinfo.ReadFile(filepath.Join(root, name))
		if e != nil {
			return k, e
		}
		settings := map[string]string{}
		for _, s := range info.Settings {
			settings[s.Key] = s.Value
		}
		if info.Path != wantPath || settings["GOOS"] != "linux" || settings["GOARCH"] != runtime.GOARCH {
			return k, fmt.Errorf("wrong executable identity/platform: %s", name)
		}
		if component != "claude-live" {
			values := map[string]string{}
			flags := strings.Fields(settings["-ldflags"])
			for i := 0; i+1 < len(flags); i++ {
				if flags[i] == "-X" {
					parts := strings.SplitN(flags[i+1], "=", 2)
					if len(parts) == 2 {
						values[parts[0]] = parts[1]
					}
				}
			}
			if values["github.com/gabemahoney/agent-director/internal/version.Version"] != k.Version || values["github.com/gabemahoney/agent-director/internal/version.Commit"] != k.CommitStamp {
				return k, errors.New("runtime/admin build stamps differ from kit")
			}
		}
	}
	err = filepath.WalkDir(root, func(p string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		name, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if name != "kit.json" {
			if _, ok := k.Files[name]; !ok {
				return fmt.Errorf("unmanifested kit member %s", name)
			}
		}
		return nil
	})
	return k, err
}

func (r *Run) VerifyCandidate(ctx context.Context, installed bool) (Kit, error) {
	k, err := VerifyKit(r.Kit)
	if err != nil {
		return k, err
	}
	r.Manifest.Candidate = k.BaseCommit + "/" + k.SnapshotTree
	b, err := Read(filepath.Dir(r.Kit), "claude-live-kit.tar.gz")
	if err != nil {
		return k, err
	}
	r.Manifest.KitSHA256 = Digest(b)
	proof := CandidateEvidence{Schema: Schema, RunID: r.Manifest.RunID, Kit: k, KitSHA256: r.Manifest.KitSHA256, Installed: installed, ProviderVersion: r.Manifest.ProviderVersion, ProviderCommand: r.ProviderCommand}
	for _, component := range []string{"agent-director", "agent-director-admin"} {
		path := filepath.Join(r.Kit, "payload", component+"-linux-"+runtime.GOARCH)
		if installed {
			subdir := "bin"
			if component == "agent-director-admin" {
				subdir = "admin"
			}
			path = filepath.Join(r.Home, ".config/agent-director", subdir, component)
			b, err := os.ReadFile(path)
			if err != nil {
				return k, err
			}
			want := k.Files["payload/"+component+"-linux-"+runtime.GOARCH]
			if Digest(b) != want.SHA256 {
				return k, errors.New("installed candidate hash differs from kit")
			}
		}
		cmd, b, err := r.Commands.Run(ctx, "verify-"+component, 15*time.Second, path, "version")
		if err != nil {
			return k, err
		}
		var v struct {
			Version string `json:"version"`
			Commit  string `json:"commit"`
		}
		if json.Unmarshal(b, &v) != nil || v.Version != k.Version || v.Commit != k.CommitStamp {
			return k, errors.New("runtime/admin pair identity mismatch")
		}
		if component == "agent-director" {
			proof.RuntimeCommand = cmd.Stdout
		} else {
			proof.AdminCommand = cmd.Stdout
		}
	}
	name := "raw/candidate-payload.json"
	if installed {
		name = "raw/candidate-installed.json"
	}
	if _, ok := k.Files["qualification/source.json"]; ok {
		data, er := Read(r.Kit, "qualification/source.json")
		if er != nil {
			return k, er
		}
		sourceName := "raw/candidate-source-payload.json"
		if installed {
			sourceName = "raw/candidate-source-installed.json"
		}
		if er = r.Evidence.Put(sourceName, data); er != nil {
			return k, er
		}
	}
	return k, r.Evidence.JSON(name, proof)
}

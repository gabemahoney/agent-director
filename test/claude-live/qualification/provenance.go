package qualification

import (
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/gabemahoney/agent-director/test/claude-live/runner"
)

const ProductAlgorithm = "go-build-inputs-v1"

type ProductSource struct {
	Schema          int               `json:"schema"`
	Algorithm       string            `json:"algorithm"`
	Product         string            `json:"product"`
	Toolchain       string            `json:"toolchain"`
	Platform        string            `json:"platform"`
	Snapshot        string            `json:"snapshot"`
	SourceArchive   string            `json:"source_archive"`
	InventorySHA256 string            `json:"inventory_sha256"`
	Files           map[string]string `json:"files"`
}

func digestText(s string, n int) bool {
	b, err := hex.DecodeString(s)
	return err == nil && len(b) == n && s == strings.ToLower(s)
}
func ProductDigest(p ProductSource) (string, error) {
	if p.Schema != Schema || p.Algorithm != ProductAlgorithm || p.Toolchain == "" || (p.Platform != "linux/amd64" && p.Platform != "linux/arm64") || len(p.Files) == 0 {
		return "", errors.New("incomplete product build inputs")
	}
	for name, hash := range p.Files {
		if filepath.IsAbs(name) || filepath.ToSlash(filepath.Clean(name)) != name || strings.ContainsAny(name, "\\\n\r\t") || name == ".." || strings.HasPrefix(name, "../") || !digestText(hash, 32) {
			return "", errors.New("unsafe or invalid product input")
		}
	}
	for _, name := range []string{"go.mod", "go.sum"} {
		if _, ok := p.Files[name]; !ok {
			return "", errors.New("product source lacks module pins")
		}
	}
	data, err := json.Marshal(struct {
		Algorithm, Toolchain, Platform string
		Files                          map[string]string
	}{p.Algorithm, p.Toolchain, p.Platform, p.Files})
	if err != nil {
		return "", err
	}
	return runner.Digest(data), nil
}

// BuildProductSource runs only while the existing kit is built in its
// scheduler container. The offline readers below never invoke Go or a shell.
func BuildProductSource(source, inventory, graph, archive, platform, toolchain, out string) error {
	if os.Getenv("AGENT_DIRECTOR_TEST_SANDBOX") != "1" {
		return errors.New("product provenance generation is scheduler-build only")
	}
	b, err := runner.Read(filepath.Dir(inventory), filepath.Base(inventory))
	if err != nil {
		return err
	}
	var identity struct {
		Tree  string            `json:"tree"`
		Files map[string]string `json:"files"`
	}
	if err = json.Unmarshal(b, &identity); err != nil {
		return err
	}
	p := ProductSource{Schema: Schema, Algorithm: ProductAlgorithm, Toolchain: toolchain, Platform: platform, Snapshot: identity.Tree, SourceArchive: archive, InventorySHA256: runner.Digest(b), Files: map[string]string{}}
	if !digestText(p.Snapshot, 20) || !digestText(archive, 32) {
		return errors.New("unbound product snapshot/archive")
	}
	data, err := runner.Read(filepath.Dir(graph), filepath.Base(graph))
	if err != nil {
		return err
	}
	dec := json.NewDecoder(strings.NewReader(string(data)))
	for {
		var pkg struct {
			Dir                                                                                                                 string
			Standard                                                                                                            bool
			GoFiles, CgoFiles, CFiles, CXXFiles, MFiles, HFiles, FFiles, SFiles, SwigFiles, SwigCXXFiles, SysoFiles, EmbedFiles []string
			Module                                                                                                              *struct {
				Path, Version string
				Main          bool
				Replace       *struct{ Path, Version string }
			}
		}
		if err = dec.Decode(&pkg); errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return err
		}
		if pkg.Standard {
			continue
		}
		if pkg.Module == nil {
			return errors.New("non-standard package lacks module provenance")
		}
		if !pkg.Module.Main && (pkg.Module.Version == "" || (pkg.Module.Replace != nil && pkg.Module.Replace.Version == "")) {
			return errors.New("unpinned/local replacement in product build graph")
		}
		rel, er := filepath.Rel(source, pkg.Dir)
		inside := er == nil && rel != ".." && !strings.HasPrefix(rel, "../")
		if !inside {
			if pkg.Module == nil || pkg.Module.Version == "" || (pkg.Module.Replace != nil && pkg.Module.Replace.Version == "") {
				return errors.New("unbound external/local replacement in product build graph")
			}
			continue
		}
		files := append([]string{}, pkg.GoFiles...)
		for _, more := range [][]string{pkg.CgoFiles, pkg.CFiles, pkg.CXXFiles, pkg.MFiles, pkg.HFiles, pkg.FFiles, pkg.SFiles, pkg.SwigFiles, pkg.SwigCXXFiles, pkg.SysoFiles, pkg.EmbedFiles} {
			files = append(files, more...)
		}
		for _, file := range files {
			name := filepath.ToSlash(filepath.Join(rel, file))
			if strings.HasPrefix(name, "test/") {
				return errors.New("harness unexpectedly enters product build graph")
			}
			p.Files[name] = identity.Files[name]
		}
	}
	p.Files["go.mod"] = identity.Files["go.mod"]
	p.Files["go.sum"] = identity.Files["go.sum"]
	for name, want := range p.Files {
		b, er := runner.Read(source, name)
		if er != nil {
			return er
		}
		if runner.Digest(b) != want {
			return fmt.Errorf("product build input differs from frozen inventory: %s", name)
		}
	}
	p.Product, err = ProductDigest(p)
	if err != nil {
		return err
	}
	b, err = json.MarshalIndent(p, "", "  ")
	if err != nil {
		return err
	}
	f, err := os.OpenFile(out, os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return err
	}
	_, writeErr := f.Write(append(b, '\n'))
	return errors.Join(writeErr, f.Sync(), f.Close())
}

func LoadProductSource(root, ref string) (ProductSource, error) {
	var p ProductSource
	b, err := runner.Read(root, ref)
	if err != nil {
		return p, err
	}
	if err = json.Unmarshal(b, &p); err != nil {
		return p, err
	}
	digest, err := ProductDigest(p)
	if err != nil {
		return p, err
	}
	if p.Product != digest || !digestText(p.Snapshot, 20) || !digestText(p.SourceArchive, 32) || !digestText(p.InventorySHA256, 32) {
		return p, errors.New("product fingerprint/provenance mismatch")
	}
	return p, nil
}

func VerifyProductKit(p ProductSource, k runner.Kit) error {
	if p.Snapshot != k.SnapshotTree || p.SourceArchive != k.SourceSHA256 || p.Platform != k.Platform {
		return errors.New("product inputs belong to another frozen kit source")
	}
	return nil
}

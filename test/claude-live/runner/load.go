package runner

import (
	"encoding/json"
	"errors"
	"fmt"
)

// ReadManifest is the shared file-only envelope/index loader. Specialized
// analyzers retain their own stricter identities and behavioral contracts.
func ReadManifest(root string) (Manifest, error) {
	var m Manifest
	b, err := Read(root, "manifest.json")
	if err != nil {
		return m, err
	}
	if err = json.Unmarshal(b, &m); err != nil {
		return m, err
	}
	if m.Schema != Schema || m.Artifacts == nil {
		return m, errors.New("unsupported or incomplete evidence manifest")
	}
	for name, want := range m.Artifacts {
		b, er := Read(root, name)
		if er != nil {
			return m, er
		}
		if Digest(b) != want.SHA256 || int64(len(b)) != want.Bytes {
			return m, fmt.Errorf("artifact integrity: %s", name)
		}
	}
	return m, nil
}

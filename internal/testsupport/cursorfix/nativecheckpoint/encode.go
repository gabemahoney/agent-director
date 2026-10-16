// Package nativecheckpoint encodes the pinned source-derived synthetic native fixture.
package nativecheckpoint

import (
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Encode shares native DDL/protobuf serialization between unit and shell fixtures.
// The caller owns execution; shell collectors execute SQL only through sql.sh.
func Encode(cwd, id string) (string, []byte, error) {
	if !filepath.IsAbs(cwd) || id == "" || filepath.Base(id) != id || id == "." || id == ".." {
		return "", nil, fmt.Errorf("native fixture requires absolute cwd and single-component session ID")
	}
	var sql strings.Builder
	sql.WriteString(`PRAGMA journal_mode=WAL;
PRAGMA synchronous=NORMAL;
PRAGMA busy_timeout=5000;
PRAGMA user_version=1;
CREATE TABLE IF NOT EXISTS blobs (id TEXT PRIMARY KEY, data BLOB);
CREATE TABLE IF NOT EXISTS meta (key TEXT PRIMARY KEY, value TEXT);
`)
	put := func(data []byte) []byte {
		sum := sha256.Sum256(data)
		fmt.Fprintf(&sql, "INSERT OR REPLACE INTO blobs(id,data) VALUES('%s',X'%s');\n", hex.EncodeToString(sum[:]), hex.EncodeToString(data))
		return sum[:]
	}
	// agent_pb: user(text=1,message_id=2), agent turn(user_message=1), turn(oneof=1), root(turns=8).
	user := put(append(field(1, []byte("Synthetic retained user text for "+id)), field(2, []byte("synthetic-message-"+id))...))
	turn := put(field(1, field(1, user)))
	root := put(field(8, turn))
	meta, err := json.Marshal(map[string]any{"agentId": id, "latestRootBlobId": hex.EncodeToString(root), "name": "Synthetic " + id, "createdAt": int64(1700000000000), "mode": "default", "isRunEverything": false})
	if err != nil {
		return "", nil, err
	}
	fmt.Fprintf(&sql, "INSERT OR REPLACE INTO meta(key,value) VALUES('0','%s');\n", hex.EncodeToString(meta))
	metadata, err := json.Marshal(map[string]any{"schemaVersion": 1, "cwd": cwd, "title": "Synthetic " + id})
	return sql.String(), metadata, err
}

func field(number uint64, value []byte) []byte {
	buf := binary.AppendUvarint(nil, number<<3|2)
	buf = binary.AppendUvarint(buf, uint64(len(value)))
	return append(buf, value...)
}

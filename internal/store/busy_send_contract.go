package store

import (
	"crypto/sha256"
	"encoding/binary"
	"fmt"
	"sort"
	"strconv"
)

// BusySendCoreContractIdentity describes the compiled permanent contract,
// without opening a store. The framed, name-sorted digest includes both the
// core objects and spawns columns recognized by recognizeBusySendCore. It is
// deliberately separate from the historical test pin for the core objects.
func BusySendCoreContractIdentity() (version, declarationsSHA256 string) {
	type declaration struct {
		name   string
		fields []string
	}
	declarations := make([]declaration, 0, len(busySendCoreObjects)+len(busySendSpawnColumns))
	for _, object := range busySendCoreObjects {
		declarations = append(declarations, declaration{object.name, []string{object.kind, normalizedDDL(object.ddl)}})
	}
	for _, column := range busySendSpawnColumns {
		declarations = append(declarations, declaration{"spawns." + column.name, []string{"column", normalizedDDL(column.ddl), column.kind, strconv.FormatBool(column.notNull), strconv.FormatBool(column.def.Valid), column.def.String}})
	}
	sort.Slice(declarations, func(i, j int) bool { return declarations[i].name < declarations[j].name })
	h := sha256.New()
	for _, d := range declarations {
		for _, field := range append([]string{d.name}, d.fields...) {
			var size [8]byte
			binary.BigEndian.PutUint64(size[:], uint64(len(field)))
			h.Write(size[:])
			h.Write([]byte(field))
		}
	}
	return busySendCoreVersion, fmt.Sprintf("%x", h.Sum(nil))
}

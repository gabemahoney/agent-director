package store

import (
	"slices"
	"testing"
)

// Serial: mutations of compiled declarations must finish before parallel store tests run.
// This is separate from the historical 25-object SQL digest pin.
func TestBusySendCoreContractIdentity(t *testing.T) {
	objects, columns := busySendCoreObjects, busySendSpawnColumns
	t.Cleanup(func() { busySendCoreObjects, busySendSpawnColumns = objects, columns })
	version, digest := BusySendCoreContractIdentity()
	if version != "busy-send-core-v1:" || digest != "45bbc95a6429e2f483f3ab4bc1cc0b8d12d40f607e06b6bf4416d87fff1cb7f7" {
		t.Fatalf("identity = %q, %q", version, digest)
	}
	check := func(t *testing.T, changed bool) {
		t.Helper()
		v, got := BusySendCoreContractIdentity()
		if v != version || (got != digest) != changed {
			t.Fatalf("identity = (%q, %q), baseline (%q, %q), changed=%v", v, got, version, digest, changed)
		}
	}
	check(t, false)
	busySendCoreObjects, busySendSpawnColumns = slices.Clone(objects), slices.Clone(columns)
	slices.Reverse(busySendCoreObjects)
	slices.Reverse(busySendSpawnColumns)
	for i := range busySendCoreObjects {
		busySendCoreObjects[i].ddl = "\n " + busySendCoreObjects[i].ddl + " \t"
	}
	check(t, false)
	for i, object := range objects {
		for _, field := range []string{"name", "kind", "ddl", "removed"} {
			t.Run(object.name+"/"+field, func(t *testing.T) {
				busySendCoreObjects, busySendSpawnColumns = slices.Clone(objects), columns
				switch field {
				case "name":
					busySendCoreObjects[i].name += "_changed"
				case "kind":
					busySendCoreObjects[i].kind = "changed"
				case "ddl":
					busySendCoreObjects[i].ddl += " changed"
				case "removed":
					busySendCoreObjects = append(busySendCoreObjects[:i], busySendCoreObjects[i+1:]...)
				}
				check(t, true)
			})
		}
	}
	for i, column := range columns {
		for _, field := range []string{"name", "ddl", "kind", "nullable", "default-presence", "default-value", "removed"} {
			t.Run(column.name+"/"+field, func(t *testing.T) {
				busySendCoreObjects, busySendSpawnColumns = objects, slices.Clone(columns)
				c := &busySendSpawnColumns[i]
				switch field {
				case "name":
					c.name += "_changed"
				case "ddl":
					c.ddl += " changed"
				case "kind":
					c.kind = "BLOB"
				case "nullable":
					c.notNull = !c.notNull
				case "default-presence":
					c.def.Valid = !c.def.Valid
				case "default-value":
					c.def.String += " changed"
				case "removed":
					busySendSpawnColumns = append(busySendSpawnColumns[:i], busySendSpawnColumns[i+1:]...)
				}
				check(t, true)
			})
		}
	}
	busySendCoreObjects, busySendSpawnColumns = objects, columns
	check(t, false)
}

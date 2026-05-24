package envelope_diff

import "testing"

func TestSelectorExactPath(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		selector string
		want     bool
	}{
		{"simple match", ".foo.bar", "foo.bar", true},
		{"leading dot in selector tolerated", ".foo.bar", ".foo.bar", true},
		{"single field", ".version", "version", true},
		{"empty both", "", "", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pathMatchesSelector(tc.path, tc.selector)
			if got != tc.want {
				t.Errorf("pathMatchesSelector(%q, %q) = %v, want %v", tc.path, tc.selector, got, tc.want)
			}
		})
	}
}

func TestSelectorArrayIndex(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		selector string
		want     bool
	}{
		{"index 0 exact match", ".items[0].name", "items[0].name", true},
		{"index 1 no match for 0", ".items[1].name", "items[0].name", false},
		{"deep nested index", ".a.b[2].c", "a.b[2].c", true},
		{"index only field", ".ids[3]", "ids[3]", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pathMatchesSelector(tc.path, tc.selector)
			if got != tc.want {
				t.Errorf("pathMatchesSelector(%q, %q) = %v, want %v", tc.path, tc.selector, got, tc.want)
			}
		})
	}
}

func TestSelectorWildcardArray(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		selector string
		want     bool
	}{
		{"wildcard matches index 0", ".items[0].id", "items[*].id", true},
		{"wildcard matches index 1", ".items[1].id", "items[*].id", true},
		{"wildcard matches large index", ".items[99].id", "items[*].id", true},
		{"wildcard wrong field", ".items[0].name", "items[*].id", false},
		{"wildcard wrong parent", ".things[0].id", "items[*].id", false},
		{"wildcard array-only", ".ids[5]", "ids[*]", true},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := pathMatchesSelector(tc.path, tc.selector)
			if got != tc.want {
				t.Errorf("pathMatchesSelector(%q, %q) = %v, want %v", tc.path, tc.selector, got, tc.want)
			}
		})
	}
}

func TestSelectorNoMatch(t *testing.T) {
	cases := []struct {
		name     string
		path     string
		selector string
	}{
		{"different leaf field", ".foo.bar", "foo.baz"},
		{"path is prefix of selector", ".foo.bar", "foo.bar.baz"},
		{"selector is prefix of path", ".foo.bar.baz", "foo.bar"},
		{"suffix only", ".bar", "foo.bar"},
		{"array vs plain field", ".foo[0]", "foo"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if pathMatchesSelector(tc.path, tc.selector) {
				t.Errorf("pathMatchesSelector(%q, %q) should NOT match", tc.path, tc.selector)
			}
		})
	}
}

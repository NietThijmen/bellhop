package artifacts

import (
	"context"
	"slices"
	"strings"
	"testing"
)

func TestEmbeddedLoader(t *testing.T) {
	m, err := EmbeddedLoader().Load(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if m.Schema != SchemaVersion {
		t.Errorf("schema = %d", m.Schema)
	}
}

const sha = "0000000000000000000000000000000000000000000000000000000000000000"

func entryJSON(comp, ver, goos, arch string) string {
	return `{"component":"` + comp + `","version":"` + ver + `","os":"` + goos + `","arch":"` + arch +
		`","url":"https://example.invalid/x","sha256":"` + sha + `","format":"tar.xz","stripPrefix":"x/"}`
}

func TestParseFindVersions(t *testing.T) {
	doc := `{"schema":1,"artifacts":[` + strings.Join([]string{
		entryJSON("php", "8.10.0", "linux", "amd64"),
		entryJSON("php", "8.3.1", "linux", "amd64"),
		entryJSON("php", "8.4.0", "linux", "amd64"),
		entryJSON("php", "8.4.0", "darwin", "arm64"),
		entryJSON("mailpit", "1.20.0", "linux", "amd64"),
	}, ",") + `]}`
	m, err := Parse([]byte(doc))
	if err != nil {
		t.Fatal(err)
	}
	if got := m.Versions("php", "linux", "amd64"); !slices.Equal(got, []string{"8.3.1", "8.4.0", "8.10.0"}) {
		t.Errorf("Versions = %v", got)
	}
	if got := m.Versions("php", "windows", "amd64"); len(got) != 0 {
		t.Errorf("Versions(windows) = %v", got)
	}
	e, ok := m.Find("php", "8.4.0", "darwin", "arm64")
	if !ok || e.StripPrefix != "x/" || e.Format != FormatTarXz {
		t.Errorf("Find = %+v, %v", e, ok)
	}
	if _, ok := m.Find("php", "8.4.0", "darwin", "amd64"); ok {
		t.Error("Find matched wrong arch")
	}
}

func TestParseErrors(t *testing.T) {
	cases := map[string]string{
		"bad json":      `{`,
		"wrong schema":  `{"schema":2,"artifacts":[]}`,
		"unknown field": `{"schema":1,"artifacts":[],"extra":1}`,
		"duplicate":     `{"schema":1,"artifacts":[` + entryJSON("php", "1", "l", "a") + "," + entryJSON("php", "1", "l", "a") + `]}`,
		"bad entry":     `{"schema":1,"artifacts":[` + entryJSON("php", "..", "l", "a") + `]}`,
	}
	for name, doc := range cases {
		t.Run(name, func(t *testing.T) {
			if _, err := Parse([]byte(doc)); err == nil {
				t.Fatal("expected error")
			}
		})
	}
}

func TestCompareVersions(t *testing.T) {
	cases := []struct {
		a, b string
		want int
	}{
		{"8.3.1", "8.3.1", 0},
		{"8.9", "8.10", -1},
		{"8.10.0", "8.9.9", 1},
		{"1.0", "1.0.1", -1},
		{"1.0-rc1", "1.0.0", -1},
		{"11.4.2", "10.11.9", 1},
	}
	for _, c := range cases {
		if got := CompareVersions(c.a, c.b); got != c.want {
			t.Errorf("CompareVersions(%q, %q) = %d, want %d", c.a, c.b, got, c.want)
		}
	}
}

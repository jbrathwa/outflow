package locdata

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/jbrathwa/outflow/internal/location"
)

const cloudHeader = "prefix,provider,region,service,global\n"

var fetched = time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)

func testSources() []Source {
	var out []Source
	for _, n := range location.SourceNames {
		age := 7 * day
		if n == "geo" {
			age = 30 * day
		}
		out = append(out, Source{Name: n, Label: location.SourceLabel(n), MaxAge: age})
	}
	return out
}

func azureData(version string) Dataset {
	return Dataset{Version: version, Files: map[string][]byte{
		"cloud.csv": []byte(cloudHeader + "192.0.2.0/24,azure,westeurope,Storage,false\n"),
	}}
}

func cacheAt(t *testing.T, now time.Time) Cache {
	t.Helper()
	return Cache{Dir: t.TempDir(), Now: func() time.Time { return now }}
}

func TestSaveThenLoad(t *testing.T) {
	c := cacheAt(t, fetched)
	if err := c.Save("azure", azureData("20261102")); err != nil {
		t.Fatal(err)
	}
	db, issues, err := c.Load(testSources())
	if err != nil || len(issues) != 0 {
		t.Fatalf("err=%v issues=%v", err, issues)
	}
	got := db.Sources()
	if len(got) != 5 {
		t.Fatalf("sources = %d", len(got))
	}
	want := location.SourceInfo{Name: "azure", Version: "20261102", Fetched: fetched}
	if got[0] != want {
		t.Fatalf("azure = %+v, want %+v", got[0], want)
	}
	for _, s := range got[1:] {
		if !s.Sample {
			t.Errorf("%s should be sample", s.Name)
		}
	}
}

func TestLoadStale(t *testing.T) {
	c := cacheAt(t, fetched)
	if err := c.Save("azure", azureData("v")); err != nil {
		t.Fatal(err)
	}
	geo := Dataset{Version: "g", Files: map[string][]byte{
		"geo.csv": []byte("start_ip,end_ip,country\n192.0.2.0,192.0.2.255,NL\n"),
	}}
	if err := c.Save("geo", geo); err != nil {
		t.Fatal(err)
	}
	c.Now = func() time.Time { return fetched.Add(8 * day) }
	db, _, err := c.Load(testSources())
	if err != nil {
		t.Fatal(err)
	}
	by := map[string]location.SourceInfo{}
	for _, s := range db.Sources() {
		by[s.Name] = s
	}
	if a := by["azure"]; !a.Stale || a.AgeDays != 8 {
		t.Errorf("azure = %+v", a)
	}
	if g := by["geo"]; g.Stale || g.AgeDays != 8 || g.Sample {
		t.Errorf("geo = %+v", g)
	}
}

func TestSaveReplacesAndCleansUp(t *testing.T) {
	c := cacheAt(t, fetched)
	for _, v := range []string{"one", "two"} {
		if err := c.Save("azure", azureData(v)); err != nil {
			t.Fatal(err)
		}
	}
	dirs, _ := filepath.Glob(filepath.Join(c.Dir, "azure-*"))
	if len(dirs) != 1 {
		t.Fatalf("azure folders = %v", dirs)
	}
	db, _, err := c.Load(testSources())
	if err != nil {
		t.Fatal(err)
	}
	if v := db.Sources()[0].Version; v != "two" {
		t.Fatalf("version = %q", v)
	}
}

func TestSaveEmptyDatasetFails(t *testing.T) {
	c := cacheAt(t, fetched)
	if err := c.Save("azure", Dataset{Version: "x"}); err == nil {
		t.Fatal("expected error")
	}
	if _, ok := c.Meta("azure"); ok {
		t.Fatal("pointer must not exist")
	}
}

func TestLoadCorruptSourceFallsBack(t *testing.T) {
	t.Run("missing folder", func(t *testing.T) {
		c := cacheAt(t, fetched)
		if err := c.Save("azure", azureData("v")); err != nil {
			t.Fatal(err)
		}
		m, _ := c.Meta("azure")
		if err := os.RemoveAll(filepath.Join(c.Dir, m.Dir)); err != nil {
			t.Fatal(err)
		}
		checkFallback(t, c)
	})
	t.Run("truncated csv", func(t *testing.T) {
		c := cacheAt(t, fetched)
		if err := c.Save("azure", azureData("v")); err != nil {
			t.Fatal(err)
		}
		m, _ := c.Meta("azure")
		p := filepath.Join(c.Dir, m.Dir, "cloud.csv")
		if err := os.WriteFile(p, []byte(cloudHeader+"192.0.2.0/24,azu"), 0o600); err != nil {
			t.Fatal(err)
		}
		checkFallback(t, c)
	})
}

func checkFallback(t *testing.T, c Cache) {
	t.Helper()
	db, issues, err := c.Load(testSources())
	if err != nil {
		t.Fatal(err)
	}
	if len(issues) != 1 || issues[0].Source != "azure" || issues[0].Err == nil {
		t.Fatalf("issues = %+v", issues)
	}
	got := db.Sources()
	if !got[0].Sample || got[0].Name != "azure" {
		t.Errorf("azure = %+v", got[0])
	}
	for _, s := range got {
		if !s.Sample {
			t.Errorf("%s should be sample", s.Name)
		}
	}
}

func TestDefaultDirEnv(t *testing.T) {
	d := t.TempDir()
	t.Setenv("OUTFLOW_CACHE_DIR", d)
	got, err := DefaultDir()
	if err != nil || got != d {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestMetaRejectsTraversalPointer(t *testing.T) {
	c := cacheAt(t, fetched)
	ptr := `{"source":"azure","version":"v","fetched":"2026-11-02T12:00:00Z","dir":"../x"}`
	if err := os.WriteFile(filepath.Join(c.Dir, "azure.json"), []byte(ptr), 0o600); err != nil {
		t.Fatal(err)
	}
	_, issues, err := c.Load(testSources())
	if err != nil || len(issues) != 1 || !strings.Contains(issues[0].Err.Error(), "invalid cache folder") {
		t.Fatalf("err=%v issues=%v", err, issues)
	}
}

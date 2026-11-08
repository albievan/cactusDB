package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestParseSizes(t *testing.T) {
	for in, want := range map[string]int64{
		"512B": 512, "39.5MiB": 39.5 * (1 << 20), "1.5GiB": 1.5 * (1 << 30), "1.2kB": 1200,
		"120Mi": 120 << 20, "10Gi": 10 << 30, "500M": 500e6, "0B": 0, "": -1, "lots": -1, "12XB": -1,
	} {
		if got := parseSize(in); got != want {
			t.Errorf("parseSize(%q) = %d, want %d", in, got, want)
		}
	}
	for in, want := range map[string]float64{"250m": 25, "2": 200, "1500000n": 0.15, "0.5": 50} {
		if got := parseCPU(in); got != want {
			t.Errorf("parseCPU(%q) = %v, want %v", in, got, want)
		}
	}
	if n := duOutput("du: cannot read 'x'\n1234\t/var/lib/data\n"); n != 1234*1024 {
		t.Errorf("duOutput = %d", n)
	}
	if n := duOutput(""); n != -1 {
		t.Errorf("duOutput(empty) = %d", n)
	}
}

func TestMaskEnv(t *testing.T) {
	for _, k := range []string{"MSSQL_SA_PASSWORD", "POSTGRES_PASSWORD", "AUTHENTICATION_APIKEY_ALLOWED_KEYS", "NEO4J_AUTH", "MY_TOKEN"} {
		if v := maskEnv(k, "s3cret"); strings.Contains(v, "s3cret") {
			t.Errorf("%s shown: %s", k, v)
		}
	}
	if v := maskEnv("ACCEPT_EULA", "Y"); v != "Y" {
		t.Errorf("ACCEPT_EULA = %q", v)
	}
}

func TestPurgeableDir(t *testing.T) {
	home, _ := os.UserHomeDir()
	wd, _ := os.Getwd()
	root := "/"
	if runtime.GOOS == "windows" {
		root = `C:\`
	}
	for _, dir := range []string{root, filepath.Join(root, "srv"), home, filepath.Dir(home), wd, filepath.Dir(wd), "relative/path"} {
		if err := purgeableDir(dir); err == nil {
			t.Errorf("purgeableDir(%q) allowed", dir)
		}
	}
	for _, dir := range []string{filepath.Join(root, "srv", "cactusdb-data", "pg"), filepath.Join(home, "cactusdb-data", "pg"), filepath.Join(wd, "data")} {
		if err := purgeableDir(dir); err != nil {
			t.Errorf("purgeableDir(%q): %v", dir, err)
		}
	}
}

func TestKubePurgePod(t *testing.T) {
	var pod struct {
		Metadata struct{ Name, Namespace string }
		Spec     struct {
			NodeName   string
			Containers []struct {
				Image   string
				Command []string
			}
			Volumes []struct{ HostPath struct{ Path, Type string } }
		}
	}
	if err := json.Unmarshal(kubePurgePod("cdb-pg", "cactusdb", "node-1", "/srv/cactusdb-data/cdb-pg"), &pod); err != nil {
		t.Fatal(err)
	}
	if pod.Metadata.Name != "cdb-pg-purge" || pod.Metadata.Namespace != "cactusdb" || pod.Spec.NodeName != "node-1" {
		t.Errorf("pod = %+v", pod)
	}
	if got := strings.Join(pod.Spec.Containers[0].Command, " "); got != "rm -rf -- /parent/cdb-pg" {
		t.Errorf("command = %q", got)
	}
	if v := pod.Spec.Volumes[0].HostPath; v.Path != "/srv/cactusdb-data" || v.Type != "Directory" {
		t.Errorf("volume = %+v", v)
	}
}

func TestNeedsDatabase(t *testing.T) {
	for _, c := range []struct {
		p    ConnParams
		need bool
	}{
		{ConnParams{Engine: EngineSQLite}, true},
		{ConnParams{Engine: EngineAccess}, true},
		{ConnParams{Engine: EngineFirebird, Host: "db"}, true},
		{ConnParams{Engine: EngineOracle, Host: "db"}, true},
		{ConnParams{Engine: EngineInformix, Host: "db"}, true},
		{ConnParams{Engine: EngineFirebird, Host: "db", Database: "/data/x.fdb"}, false},
		{ConnParams{Engine: EngineMySQL, Host: "db"}, false},
		{ConnParams{Engine: EnginePostgres, Host: "db"}, false},
		{ConnParams{Engine: EngineCassandra, Host: "db"}, false},
	} {
		if err := needsDatabase(c.p); (err != nil) != c.need {
			t.Errorf("%s %q: needsDatabase = %v", c.p.Engine, c.p.Database, err)
		}
	}
	for id, want := range map[string]bool{EngineMySQL: true, EngineMariaDB: true, EnginePostgres: true, EngineRedshift: true, EngineCockroach: true,
		EngineMSSQL: true, EngineAzureSQL: true, EngineMongoDB: true, EngineRedis: true, EngineNeo4j: true, EngineSQLite: false,
		EngineFirebird: false, EngineOracle: false, EngineWeaviate: false, EngineAccess: false} {
		if got := engineByID(id).ListsDBs; got != want {
			t.Errorf("%s ListsDBs = %v, want %v", id, got, want)
		}
	}
}

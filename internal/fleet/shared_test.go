package fleet

import (
	"bytes"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/Nano112/codexfleet/internal/config"
)

func sharedFleet(t *testing.T, url, extra string) *Fleet {
	t.Helper()
	dir := t.TempDir()
	doc := "prompt_template = \"p.md\"\n[mcp_servers.re]\nurl = \"" + url + "\"\n" + extra + "\n[[tasks]]\nname = \"a\"\n"
	os.WriteFile(filepath.Join(dir, "p.md"), []byte("x"), 0o644)
	os.WriteFile(filepath.Join(dir, config.DefaultFileName), []byte(doc), 0o644)
	cfg, err := config.Load(filepath.Join(dir, config.DefaultFileName))
	if err != nil {
		t.Fatal(err)
	}
	return &Fleet{Cfg: cfg, Out: &bytes.Buffer{}}
}

func TestProbeSharedTreatsAnyReplyAsRunningUnlessHealthURLIsSet(t *testing.T) {
	var auth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		auth = r.Header.Get("Authorization")
		if r.URL.Path == "/health" {
			w.WriteHeader(http.StatusServiceUnavailable)
			return
		}
		w.WriteHeader(http.StatusBadRequest) // an MCP endpoint answers a bare GET with 4xx
	}))
	defer srv.Close()
	t.Setenv("RE_TOKEN", "secret-token")
	if h := ProbeShared(config.SharedServer{URL: srv.URL + "/mcp", BearerTokenEnv: "RE_TOKEN"}); !h.Running {
		t.Fatalf("4xx from the MCP URL should count as running: %+v", h)
	}
	if auth != "Bearer secret-token" {
		t.Fatalf("probe did not send the bearer token: %q", auth)
	}
	if h := ProbeShared(config.SharedServer{URL: srv.URL + "/mcp", HealthURL: srv.URL + "/health"}); h.Running || strings.Contains(h.Detail, "secret") {
		t.Fatalf("a non-2xx health_url must not count as running: %+v", h)
	}
	if h := ProbeShared(config.SharedServer{URL: "http://127.0.0.1:1/mcp"}); h.Running {
		t.Fatalf("a closed port is not running: %+v", h)
	}
}

func TestEnsureSharedLeavesARunningServerAlone(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	marker := filepath.Join(t.TempDir(), "started")
	f := sharedFleet(t, srv.URL+"/mcp", "[shared_mcp.re]\ncommand = \"sh\"\nargs = [\"-c\", \"touch "+marker+"\"]\n")
	if err := f.EnsureShared([]string{"re"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(marker); err == nil {
		t.Fatal("the start command ran although the server answered")
	}
}

func TestEnsureSharedStartsAServerAndWaitsForIt(t *testing.T) {
	// Reserve a port, then serve on it only once the start command has run.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	marker := filepath.Join(t.TempDir(), "started")
	f := sharedFleet(t, "http://"+addr+"/mcp",
		"[shared_mcp.re]\ncommand = \"sh\"\nargs = [\"-c\", \"echo $GREETING > "+marker+"\"]\nenv = { GREETING = \"hello\" }\nhealth_url = \"http://"+addr+"/health\"\nstart_timeout_sec = 10\n")
	done := make(chan struct{})
	go func() {
		defer close(done)
		for i := 0; i < 400; i++ {
			if _, err := os.Stat(marker); err == nil {
				break
			}
			time.Sleep(25 * time.Millisecond)
		}
		ln, err := net.Listen("tcp", addr)
		if err != nil {
			t.Error(err)
			return
		}
		srv := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {})}
		go srv.Serve(ln)
		t.Cleanup(func() { srv.Close() })
	}()
	if err := f.EnsureShared([]string{"re"}); err != nil {
		t.Fatal(err)
	}
	<-done
	if got, _ := os.ReadFile(marker); strings.TrimSpace(string(got)) != "hello" {
		t.Fatalf("start command env not applied: %q", got)
	}
	if out := f.Out.(*bytes.Buffer).String(); !strings.Contains(out, "shared MCP server ready") {
		t.Fatalf("missing ready line: %s", out)
	}
}

func TestEnsureSharedReportsAStartCommandThatFails(t *testing.T) {
	f := sharedFleet(t, "http://127.0.0.1:1/mcp", "[shared_mcp.re]\ncommand = \"sh\"\nargs = [\"-c\", \"echo cannot bind >&2; exit 3\"]\nstart_timeout_sec = 10\n")
	err := f.EnsureShared([]string{"re"})
	if err == nil || !strings.Contains(err.Error(), "cannot bind") {
		t.Fatalf("want the failing command's log, got %v", err)
	}
}

func TestDoctorChecksURLServers(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	defer srv.Close()
	level := func(f *Fleet) (string, string) {
		for _, c := range Doctor(f.Cfg) {
			if c.Name == "mcp re" {
				return c.Level, c.Msg
			}
		}
		return "", ""
	}
	if l, m := level(sharedFleet(t, srv.URL+"/mcp", "")); l != "ok" {
		t.Errorf("running server: %s %s", l, m)
	}
	if l, m := level(sharedFleet(t, "http://127.0.0.1:1/mcp", "")); l != "fail" {
		t.Errorf("unreachable server without shared_mcp: %s %s", l, m)
	}
	if l, m := level(sharedFleet(t, "http://127.0.0.1:1/mcp", "[shared_mcp.re]\ncommand = \"sh\"\n")); l != "warn" || !strings.Contains(m, "launch starts it") {
		t.Errorf("unreachable shared server: %s %s", l, m)
	}
	os.Unsetenv("RE_UNSET_TOKEN")
	if l, m := level(sharedFleet(t, srv.URL+"/mcp", "bearer_token_env_var = \"RE_UNSET_TOKEN\"\n")); l != "fail" || !strings.Contains(m, "RE_UNSET_TOKEN") {
		t.Errorf("missing token variable: %s %s", l, m)
	}
}

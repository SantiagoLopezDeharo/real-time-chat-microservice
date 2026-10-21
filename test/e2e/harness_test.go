//go:build e2e

package e2e

import (
	"bytes"
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

const mongoImage = "mongo:7.0"

var (
	image          = os.Getenv("E2E_IMAGE")
	raceBinary     string
	perfBinary     string
	coverDir       string
	mongoAddr      string
	mongoContainer string
	httpClient     = &http.Client{
		Timeout:   15 * time.Second,
		Transport: &http.Transport{MaxIdleConns: 512, MaxIdleConnsPerHost: 512},
	}
)

func TestMain(m *testing.M) {
	os.Exit(runSuite(m))
}

func runSuite(m *testing.M) int {
	workDir, err := os.MkdirTemp("", "chat-e2e-")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer os.RemoveAll(workDir)

	coverDir = filepath.Join(workDir, "cover")
	if err := os.Mkdir(coverDir, 0o755); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	stopMongo, err := startMongo()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	defer stopMongo()

	switch {
	case image != "":
	case os.Getenv("E2E_BINARY") != "":
		raceBinary, perfBinary = os.Getenv("E2E_BINARY"), os.Getenv("E2E_BINARY")
	default:
		raceBinary = filepath.Join(workDir, "server-race")
		perfBinary = filepath.Join(workDir, "server")
		if err := build(raceBinary, "-race", "-cover", "-coverpkg=chat-microservice/..."); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
		if err := build(perfBinary, "-trimpath", "-ldflags=-s -w"); err != nil {
			fmt.Fprintln(os.Stderr, err)
			return 1
		}
	}

	code := m.Run()
	reportCoverage()
	return code
}

// startMongo uses E2E_MONGO_ADDR when set, otherwise a throwaway container.
// Every server gets its own database, so one instance serves the whole run.
func startMongo() (func(), error) {
	if addr := os.Getenv("E2E_MONGO_ADDR"); addr != "" {
		mongoAddr = addr
		return func() {}, nil
	}
	out, err := exec.Command("docker", "run", "-d", "--rm", "-p", "127.0.0.1::27017", mongoImage).Output()
	if err != nil {
		return nil, fmt.Errorf("start %s: %w", mongoImage, err)
	}
	mongoContainer = strings.TrimSpace(string(out))
	stop := func() { _ = exec.Command("docker", "rm", "-f", mongoContainer).Run() }
	port, err := exec.Command("docker", "port", mongoContainer, "27017/tcp").Output()
	if err != nil {
		stop()
		return nil, fmt.Errorf("mongo port: %w", err)
	}
	mongoAddr = strings.TrimSpace(strings.Split(string(port), "\n")[0])

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		logs, _ := exec.Command("docker", "logs", mongoContainer).CombinedOutput()
		if bytes.Contains(logs, []byte("Waiting for connections")) {
			return stop, nil
		}
		time.Sleep(200 * time.Millisecond)
	}
	stop()
	return nil, fmt.Errorf("%s did not become ready", mongoImage)
}

func build(output string, flags ...string) error {
	args := append([]string{"build", "-o", output}, flags...)
	args = append(args, "./cmd/server")
	cmd := exec.Command("go", args...)
	cmd.Dir = moduleRoot()
	cmd.Env = append(os.Environ(), "CGO_ENABLED=1")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("go %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return nil
}

func moduleRoot() string {
	_, file, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(file), "..", "..")
}

func reportCoverage() {
	entries, _ := os.ReadDir(coverDir)
	if len(entries) == 0 {
		return
	}
	if out, err := exec.Command("go", "tool", "covdata", "percent", "-i="+coverDir).CombinedOutput(); err == nil {
		fmt.Printf("\ne2e binary coverage:\n%s", out)
	}
}

type server struct {
	t         *testing.T
	cmd       *exec.Cmd
	env       map[string]string
	addr      string
	baseURL   string
	wsURL     string
	pprofURL  string
	jwtSecret string
	logs      *lockedBuffer
	exited    chan struct{}
	exitErr   error
	stopOnce  sync.Once
}

type option func(env map[string]string)

func withEnv(key, value string) option {
	return func(env map[string]string) { env[key] = value }
}

func withoutEnv(key string) option {
	return func(env map[string]string) { delete(env, key) }
}

// withDatabase reuses a database across servers, e.g. to check what a
// previous instance persisted.
func withDatabase(name string) option {
	return withEnv("MONGO_DB", name)
}

func startServer(t *testing.T, opts ...option) *server {
	return startBinary(t, raceBinary, opts...)
}

func startBinary(t *testing.T, binary string, opts ...option) *server {
	t.Helper()
	s := launch(t, binary, opts...)
	if err := s.waitReady(30 * time.Second); err != nil {
		t.Fatalf("server not ready: %v\nlogs:\n%s", err, s.logs)
	}
	t.Cleanup(s.stopAndVerify)
	return s
}

// launch starts the server binary, or the E2E_IMAGE container when set, with
// throwaway secrets, a fresh database and free host ports.
func launch(t *testing.T, binary string, opts ...option) *server {
	t.Helper()
	hostPort, pprofPort := freePort(t), freePort(t)
	env := map[string]string{
		"PORT":             strconv.Itoa(hostPort),
		"JWT_SECRET":       randomSecret(),
		"MONGO_URI":        "mongodb://" + mongoAddr,
		"MONGO_DB":         "e2e_" + randomSecret()[:12],
		"RATE_LIMIT_RPS":   "1000000",
		"RATE_LIMIT_BURST": "1000000",
		"PPROF_ADDR":       fmt.Sprintf("127.0.0.1:%d", pprofPort),
		"GORACE":           "halt_on_error=0",
		"GOCOVERDIR":       coverDir,
	}
	if image != "" {
		_, mongoPort, _ := net.SplitHostPort(mongoAddr)
		env["PORT"], env["PPROF_ADDR"] = "8080", ":6060"
		env["MONGO_URI"] = "mongodb://host.docker.internal:" + mongoPort
		delete(env, "GORACE")
		delete(env, "GOCOVERDIR")
	}
	for _, opt := range opts {
		opt(env)
	}

	s := &server{
		t:         t,
		env:       env,
		addr:      fmt.Sprintf("127.0.0.1:%d", hostPort),
		jwtSecret: env["JWT_SECRET"],
		logs:      &lockedBuffer{},
		exited:    make(chan struct{}),
	}
	s.baseURL = "http://" + s.addr
	s.wsURL = "ws://" + s.addr
	if env["PPROF_ADDR"] != "" {
		s.pprofURL = fmt.Sprintf("http://127.0.0.1:%d", pprofPort)
	}

	if image != "" {
		s.cmd = dockerRun(hostPort, pprofPort, env)
	} else {
		s.cmd = exec.Command(binary)
		s.cmd.Dir = t.TempDir()
		for k, v := range env {
			s.cmd.Env = append(s.cmd.Env, k+"="+v)
		}
	}
	s.cmd.Stdout = s.logs
	s.cmd.Stderr = s.logs
	if err := s.cmd.Start(); err != nil {
		t.Fatalf("start server: %v", err)
	}
	go func() {
		s.exitErr = s.cmd.Wait()
		close(s.exited)
	}()
	return s
}

func dockerRun(hostPort, pprofPort int, env map[string]string) *exec.Cmd {
	args := []string{"run", "--rm", "--add-host=host.docker.internal:host-gateway", "-p", fmt.Sprintf("127.0.0.1:%d:8080", hostPort)}
	if env["PPROF_ADDR"] != "" {
		args = append(args, "-p", fmt.Sprintf("127.0.0.1:%d:6060", pprofPort))
	}
	for k, v := range env {
		args = append(args, "-e", k+"="+v)
	}
	return exec.Command("docker", append(args, image)...)
}

func (s *server) waitReady(timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		select {
		case <-s.exited:
			return fmt.Errorf("server exited early: %v", s.exitErr)
		default:
		}
		if resp, err := httpClient.Get(s.baseURL + "/health"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				s.detectPprof()
				return nil
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("timeout after %s", timeout)
}

// detectPprof disables profiling for binaries that predate PPROF_ADDR, such as
// a baseline build used for performance comparisons.
func (s *server) detectPprof() {
	if s.pprofURL == "" {
		return
	}
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if resp, err := httpClient.Get(s.pprofURL + "/debug/pprof/"); err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	s.pprofURL = ""
}

// stop sends SIGTERM and waits for the process to exit.
func (s *server) stop(timeout time.Duration) error {
	var err error
	s.stopOnce.Do(func() {
		_ = s.cmd.Process.Signal(syscall.SIGTERM)
		select {
		case <-s.exited:
			err = s.exitErr
		case <-time.After(timeout):
			_ = s.cmd.Process.Kill()
			<-s.exited
			err = fmt.Errorf("server did not exit within %s", timeout)
		}
	})
	return err
}

var fatalLog = regexp.MustCompile(`WARNING: DATA RACE|panic:|fatal error:|goroutine \d+ \[running\]`)

func (s *server) stopAndVerify() {
	if err := s.stop(30 * time.Second); err != nil {
		s.t.Errorf("server exit: %v", err)
	}
	if match := fatalLog.FindString(s.logs.String()); match != "" {
		s.t.Errorf("server log contains %q", match)
	}
	if s.t.Failed() {
		s.t.Logf("server logs:\n%s", s.logs)
	}
}

func freePort(t *testing.T) int {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port
}

func randomSecret() string {
	b := make([]byte, 24)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

type lockedBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *lockedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *lockedBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

func (s *server) goroutines(t *testing.T) int {
	t.Helper()
	body := s.pprofGet(t, "/debug/pprof/goroutine?debug=1")
	line, _, _ := strings.Cut(body, "\n")
	n, err := strconv.Atoi(strings.TrimPrefix(line, "goroutine profile: total "))
	if err != nil {
		t.Fatalf("parse goroutine profile header %q", line)
	}
	return n
}

var memStatLine = regexp.MustCompile(`# (\w+) = (\d+)`)

func (s *server) memStats(t *testing.T) map[string]uint64 {
	t.Helper()
	stats := map[string]uint64{}
	for _, m := range memStatLine.FindAllStringSubmatch(s.pprofGet(t, "/debug/pprof/heap?debug=1&gc=1"), -1) {
		stats[m[1]], _ = strconv.ParseUint(m[2], 10, 64)
	}
	return stats
}

func (s *server) pprofGet(t *testing.T, path string) string {
	t.Helper()
	if s.pprofURL == "" {
		t.Fatal("pprof not enabled on this server")
	}
	resp, err := httpClient.Get(s.pprofURL + path)
	if err != nil {
		t.Fatalf("pprof %s: %v", path, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(resp.Body)
	return string(body)
}

func (s *server) rssKB(t *testing.T) int64 {
	t.Helper()
	out, err := exec.Command("ps", "-o", "rss=", "-p", strconv.Itoa(s.cmd.Process.Pid)).Output()
	if err != nil {
		t.Fatalf("ps: %v", err)
	}
	kb, _ := strconv.ParseInt(strings.TrimSpace(string(out)), 10, 64)
	return kb
}

func (s *server) waitGoroutinesBelow(t *testing.T, limit int, timeout time.Duration) int {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for {
		n := s.goroutines(t)
		if n <= limit || time.Now().After(deadline) {
			return n
		}
		time.Sleep(100 * time.Millisecond)
	}
}

// pauseMongo freezes the shared database to simulate a stalled dependency.
func pauseMongo(t *testing.T) func() {
	t.Helper()
	if mongoContainer == "" {
		t.Skip("needs the harness-managed MongoDB container")
	}
	if out, err := exec.Command("docker", "pause", mongoContainer).CombinedOutput(); err != nil {
		t.Fatalf("docker pause: %v %s", err, out)
	}
	var once sync.Once
	resume := func() { once.Do(func() { _ = exec.Command("docker", "unpause", mongoContainer).Run() }) }
	t.Cleanup(resume)
	return resume
}

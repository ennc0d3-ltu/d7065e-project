//go:build system

// Package systest runs the real processes together, on a real broker, against
// the real BuildSim, and breaks them. It is the evidence behind the test plan
// (T-series): the unit tests prove the logic, these prove that the processes
// behave when others die, lie or restart.
//
// Run (needs the BuildSim binary built from the course repo):
//
//	BUILDSIM_BIN=/path/to/buildsim go test -tags system -timeout 15m -v ./systest/
package systest

import (
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	mqtt "github.com/mochi-mqtt/server/v2"
	"github.com/mochi-mqtt/server/v2/hooks/auth"
	"github.com/mochi-mqtt/server/v2/listeners"
)

var (
	buildOnce sync.Once
	binDir    string
	buildErr  error
)

func TestMain(m *testing.M) {
	code := m.Run()
	if binDir != "" {
		_ = os.RemoveAll(binDir)
	}
	os.Exit(code)
}

// services are the binaries the rig can start.
var services = []string{"physics-sim", "sensor-gateway", "bridge", "detector", "sprinkler-actuator", "responder", "dashboard", "trainer"}

func buildBinaries(t *testing.T) string {
	t.Helper()
	buildOnce.Do(func() {
		dir, err := os.MkdirTemp("", "systest-bin-")
		if err != nil {
			buildErr = err
			return
		}
		src, _ := filepath.Abs("..")
		for _, name := range services {
			cmd := exec.Command("go", "build", "-o", filepath.Join(dir, name), "./cmd/"+name)
			cmd.Dir = src
			if out, err := cmd.CombinedOutput(); err != nil {
				buildErr = fmt.Errorf("build %s: %v\n%s", name, err, out)
				return
			}
		}
		binDir = dir
	})
	if buildErr != nil {
		t.Fatal(buildErr)
	}
	return binDir
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

// proc is one running process with its log.
type proc struct {
	t       *testing.T
	name    string
	cmd     *exec.Cmd
	port    int
	logPath string
	done    chan struct{}
}

func (p *proc) alive() bool {
	select {
	case <-p.done:
		return false
	default:
		return true
	}
}

// kill is SIGKILL: a crash, with no chance to clean up.
func (p *proc) kill() {
	if p.alive() {
		_ = p.cmd.Process.Kill()
		<-p.done
	}
}

// stop is SIGTERM: a clean shutdown.
func (p *proc) stop() {
	if !p.alive() {
		return
	}
	_ = p.cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		p.kill()
	}
}

func (p *proc) logs() string {
	b, _ := os.ReadFile(p.logPath)
	return string(b)
}

func (p *proc) url(path string) string { return "http://127.0.0.1:" + strconv.Itoa(p.port) + path }

// rig is one running system.
type rig struct {
	t          *testing.T
	bin        string
	dir        string
	bs         *proc
	mqttURL    string
	brokerAddr string
	broker     *mqtt.Server
	listen     *listeners.TCP
	procs      map[string]*proc
	rooms      []string
}

// options configure a scenario.
type options struct {
	rooms  []string // level/room keys, default A109 and A108
	fire   string   // physics FIRE spec, e.g. "level0/A109:medium:30"
	benign string   // physics BENIGN spec
	doors  string   // physics DOORS spec
	model  bool     // run the detector with a trained model
}

func newRig(t *testing.T, opt options) *rig {
	t.Helper()
	bsBin := os.Getenv("BUILDSIM_BIN")
	if bsBin == "" {
		t.Skip("BUILDSIM_BIN not set: the system tests need the BuildSim binary from the course repo")
	}
	if len(opt.rooms) == 0 {
		opt.rooms = []string{"level0/A109", "level0/A108"}
	}
	r := &rig{t: t, bin: buildBinaries(t), dir: t.TempDir(), procs: map[string]*proc{}, rooms: opt.rooms}
	t.Cleanup(r.shutdown)

	r.startBroker()
	bsPort := freePort(t)
	r.bs = r.spawn("buildsim", bsBin, bsPort, nil, "start", "--host", "127.0.0.1", "--port", strconv.Itoa(bsPort))
	r.waitReady(r.bs, "/healthz")
	return r
}

// restartBuildSim starts BuildSim again on the same port (its state is in
// memory, so everything registered with it is gone).
func (r *rig) restartBuildSim() {
	r.t.Helper()
	port := r.bs.port
	r.bs.kill()
	r.bs = r.spawn("buildsim", os.Getenv("BUILDSIM_BIN"), port, nil, "start", "--host", "127.0.0.1", "--port", strconv.Itoa(port))
	r.waitReady(r.bs, "/healthz")
}

// spawn starts a binary, wiring its log to a file.
func (r *rig) spawn(name, bin string, port int, env []string, args ...string) *proc {
	r.t.Helper()
	logPath := filepath.Join(r.dir, name+".log")
	f, err := os.Create(logPath)
	if err != nil {
		r.t.Fatal(err)
	}
	cmd := exec.Command(bin, args...)
	cmd.Env = append(os.Environ(), env...)
	cmd.Stdout, cmd.Stderr = f, f
	if err := cmd.Start(); err != nil {
		r.t.Fatalf("start %s: %v", name, err)
	}
	p := &proc{t: r.t, name: name, cmd: cmd, port: port, logPath: logPath, done: make(chan struct{})}
	go func() {
		_ = cmd.Wait()
		_ = f.Close()
		close(p.done)
	}()
	return p
}

func (r *rig) startBroker() {
	r.t.Helper()
	s := mqtt.New(&mqtt.Options{InlineClient: true})
	if err := s.AddHook(new(auth.AllowHook), nil); err != nil {
		r.t.Fatal(err)
	}
	addr := "127.0.0.1:0"
	if r.brokerAddr != "" {
		addr = r.brokerAddr // restart on the same port: clients reconnect on their own
	}
	l := listeners.NewTCP(listeners.Config{ID: "systest", Address: addr})
	if err := s.AddListener(l); err != nil {
		r.t.Fatal(err)
	}
	if err := s.Serve(); err != nil {
		r.t.Fatal(err)
	}
	r.broker, r.listen, r.mqttURL, r.brokerAddr = s, l, "tcp://"+l.Address(), l.Address()
}

// restartBroker brings the broker back on the same address after stopBroker.
func (r *rig) restartBroker() { r.startBroker() }

// stopBroker kills the broker, as a crash of the message path.
func (r *rig) stopBroker() {
	if r.broker != nil {
		_ = r.broker.Close()
		r.broker = nil // closing twice panics
	}
}

func (r *rig) shutdown() {
	for _, p := range r.procs {
		p.stop()
	}
	if r.bs != nil {
		r.bs.stop()
	}
	r.stopBroker()
	if r.t.Failed() {
		for name, p := range r.procs {
			r.t.Logf("---- %s log ----\n%s", name, tail(p.logs(), 2500))
		}
	}
}

func tail(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

func (r *rig) bsURL(path string) string { return r.bs.url(path) }

func (r *rig) waitReady(p *proc, path string) {
	r.t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for time.Now().Before(deadline) {
		if !p.alive() {
			r.t.Fatalf("%s exited during start-up:\n%s", p.name, tail(p.logs(), 2000))
		}
		if code, _ := httpStatus(p.url(path)); code == 200 {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	r.t.Fatalf("%s did not become ready:\n%s", p.name, tail(p.logs(), 2000))
}

func httpStatus(url string) (int, error) {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

func httpGet(url string) (string, error) {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Get(url)
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()
	b, err := io.ReadAll(resp.Body)
	return string(b), err
}

func httpPost(url, body string) (int, error) {
	c := http.Client{Timeout: 2 * time.Second}
	resp, err := c.Post(url, "application/json", strings.NewReader(body))
	if err != nil {
		return 0, err
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.StatusCode, nil
}

// --- starting each service with the same wiring the compose file uses ---

func roomSpecs(rooms []string) string {
	var out []string
	for _, r := range rooms {
		out = append(out, fmt.Sprintf("%s:150:%s-sprinkler", r, strings.ReplaceAll(r, "/", "-")))
	}
	return strings.Join(out, ",")
}

// start launches one named service with its standard environment.
func (r *rig) start(name string, opt options) *proc {
	r.t.Helper()
	port := freePort(r.t)
	var env []string
	listen := "LISTEN=127.0.0.1:" + strconv.Itoa(port)
	switch name {
	case "physics-sim":
		env = []string{"BUILDSIM_URL=" + r.bsURL(""), listen, "TICK_MS=20", "SIM_DT=1",
			"ROOMS=" + roomSpecs(r.rooms), "DOORS=" + opt.doors, "FIRE=" + opt.fire, "BENIGN=" + opt.benign}
	case "sensor-gateway":
		env = []string{"TRUTH_URL=" + r.procs["physics-sim"].url(""), "MQTT_URL=" + r.mqttURL, "BUILDSIM_URL=" + r.bsURL(""),
			"ROOMS=" + strings.Join(r.rooms, ","), "SAMPLE_MS=20", listen}
		if a := r.procs["sprinkler-actuator"]; a != nil {
			env = append(env, "ACTUATOR_URL="+a.url("")) // the broker-free path (D6)
		}
	case "bridge":
		env = []string{"MQTT_URL=" + r.mqttURL, "BUILDSIM_URL=" + r.bsURL(""), listen}
	case "detector":
		env = []string{"MQTT_URL=" + r.mqttURL, listen}
		if opt.model {
			env = append(env, "MODEL_PATH="+r.trainModel())
		}
	case "sprinkler-actuator":
		env = []string{"BUILDSIM_URL=" + r.bsURL(""), "MQTT_URL=" + r.mqttURL, "ROOMS=" + strings.Join(r.rooms, ","), listen}
	case "responder":
		env = []string{"MQTT_URL=" + r.mqttURL, "BUILDSIM_URL=" + r.bsURL(""), "SAFE_ROOMS=level0/A113", listen}
	case "dashboard":
		env = []string{"MQTT_URL=" + r.mqttURL, "BUILDSIM_URL=" + r.bsURL(""), "PUBLISH_MS=200", listen}
	default:
		r.t.Fatalf("unknown service %q", name)
	}
	env = append(env, "OUTBOX_DIR="+filepath.Join(r.dir, "outbox"))
	p := r.spawn(name, filepath.Join(r.bin, name), port, env)
	r.procs[name] = p
	r.waitReady(p, "/readyz")
	return p
}

// restart starts the named service again with the options it had (after a kill).
func (r *rig) restart(name string, opt options) *proc { return r.start(name, opt) }

func (r *rig) trainModel() string {
	out := filepath.Join(r.dir, "model.json")
	cmd := exec.Command(filepath.Join(r.bin, "trainer"), "-out", out)
	if b, err := cmd.CombinedOutput(); err != nil {
		r.t.Fatalf("trainer: %v\n%s", err, b)
	}
	return out
}

// startAll starts the standard stack. skip names are left out.
func (r *rig) startAll(opt options, skip ...string) {
	r.t.Helper()
	omit := map[string]bool{}
	for _, s := range skip {
		omit[s] = true
	}
	for _, name := range []string{"sprinkler-actuator", "physics-sim", "sensor-gateway", "bridge", "detector", "responder", "dashboard"} {
		if !omit[name] {
			r.start(name, opt)
		}
	}
}

// --- observation ---

func (r *rig) sprinkler(room string) string {
	body, err := httpGet(r.bsURL("/api/actuators/" + strings.ReplaceAll(room, "/", "-") + "-sprinkler"))
	if err != nil {
		return "?"
	}
	var a struct {
		State string `json:"state"`
	}
	if json.Unmarshal([]byte(body), &a) != nil {
		return "?"
	}
	return a.State
}

type truth struct {
	TempC   float64 `json:"temp_c"`
	Smoke   float64 `json:"smoke"`
	COppm   float64 `json:"co_ppm"`
	SimTime float64 `json:"sim_time"`
}

func (r *rig) truth(room string) truth {
	var tr truth
	if body, err := httpGet(r.procs["physics-sim"].url("/truth/rooms/" + room)); err == nil {
		_ = json.Unmarshal([]byte(body), &tr)
	}
	return tr
}

func (r *rig) simTime() float64 { return r.truth(r.rooms[0]).SimTime }

type bridgeStats struct {
	Written, Duplicates, Gaps, Rejected, WriteErrors, NotRegistered uint64
}

func (r *rig) bridgeStats() bridgeStats {
	var s bridgeStats
	if p := r.procs["bridge"]; p != nil {
		if body, err := httpGet(p.url("/stats")); err == nil {
			_ = json.Unmarshal([]byte(body), &s)
		}
	}
	return s
}

// waitFor polls cond until it holds or the timeout passes.
func waitFor(t *testing.T, timeout time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(50 * time.Millisecond)
	}
	t.Fatalf("timed out after %v waiting for %s", timeout, what)
}

// stays asserts cond holds continuously for the whole duration.
func stays(t *testing.T, d time.Duration, what string, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if !cond() {
			t.Fatalf("%s did not hold for %v", what, d)
		}
		time.Sleep(50 * time.Millisecond)
	}
}

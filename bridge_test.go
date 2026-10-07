package main

import (
	"bufio"
	"fmt"
	"io"
	"net"
	"strings"
	"testing"
	"time"
)

type delivery struct {
	name string
	body string
}

// startBridge runs a Server on a loopback port and returns a connected sender.
func startBridge(t *testing.T, store *Store) (net.Conn, *bufio.Reader) {
	t.Helper()
	store.Dir = t.TempDir()
	if err := store.Load(); err != nil {
		t.Fatal(err)
	}
	allow, _ := ParseAllowlist("private")
	srv := &Server{Store: store, Allow: allow, Idle: 200 * time.Millisecond, MinLines: 3, Travel: [3]float64{838, 444, 100}}
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go srv.Serve(l)
	conn, err := net.Dial("tcp", l.Addr().String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn, bufio.NewReader(conn)
}

// expect reads lines until one contains want.
func expect(t *testing.T, conn net.Conn, r *bufio.Reader, want string) {
	t.Helper()
	conn.SetReadDeadline(time.Now().Add(2 * time.Second))
	for {
		line, err := r.ReadString('\n')
		if strings.Contains(line, want) {
			return
		}
		if err != nil {
			t.Fatalf("waiting for %q: %v", want, err)
		}
	}
}

func waitStatus(t *testing.T, store *Store, want string) Job {
	t.Helper()
	for i := 0; i < 100; i++ {
		if jobs := store.Jobs(); len(jobs) > 0 && jobs[0].Status == want {
			return jobs[0]
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no job reached status %q; jobs: %+v", want, store.Jobs())
	return Job{}
}

func TestCapturesAndDeliversJob(t *testing.T) {
	got := make(chan delivery, 1)
	store := &Store{RequireEnd: true, Deliver: func(name string, body []byte) error {
		got <- delivery{name, string(body)}
		return nil
	}}
	conn, r := startBridge(t, store)
	expect(t, conn, r, "Grbl 1.1")

	fmt.Fprint(conn, "$I\n")
	expect(t, conn, r, "[VER:")
	expect(t, conn, r, "ok")
	fmt.Fprint(conn, "?")
	expect(t, conn, r, "<Idle")
	fmt.Fprint(conn, "$$\n")
	expect(t, conn, r, "$130=838.000")
	expect(t, conn, r, "ok")

	program := []string{"G21", "G90", "M3 S18000", "G0 X10 Y10", "G1 Z-1 F200", "$J=G91 X5 F500", "G1 X50 F800", "M5", "M2"}
	fmt.Fprint(conn, strings.Join(program, "\r\n")+"\r\n")
	for range program {
		expect(t, conn, r, "ok")
	}

	select {
	case d := <-got:
		want := "G21\nG90\nM3 S18000\nG0 X10 Y10\nG1 Z-1 F200\nG1 X50 F800\nM5\nM2\n"
		if d.body != want {
			t.Errorf("delivered body:\n%q\nwant:\n%q", d.body, want)
		}
		if strings.ContainsAny(d.name, ": ") {
			t.Errorf("job name %q must not contain a colon or space", d.name)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("job was not delivered")
	}
	job := waitStatus(t, store, StatusDelivered)
	if job.Lines != 8 {
		t.Errorf("lines = %d, want 8", job.Lines)
	}
}

func TestHoldsProgramWithoutEnd(t *testing.T) {
	store := &Store{RequireEnd: true, Deliver: func(string, []byte) error {
		t.Error("an incomplete job must not be delivered")
		return nil
	}}
	conn, r := startBridge(t, store)
	expect(t, conn, r, "Grbl 1.1")
	fmt.Fprint(conn, "G21\nG90\nG0 X1\nG1 X20 F500\n")
	job := waitStatus(t, store, StatusHeld)
	if job.Lines != 4 {
		t.Errorf("lines = %d, want 4", job.Lines)
	}
}

func TestIgnoresShortFragments(t *testing.T) {
	store := &Store{RequireEnd: false, Deliver: func(string, []byte) error { return nil }}
	conn, r := startBridge(t, store)
	expect(t, conn, r, "Grbl 1.1")
	fmt.Fprint(conn, "G21\nG90\n")
	expect(t, conn, r, "ok")
	expect(t, conn, r, "ok")
	time.Sleep(500 * time.Millisecond)
	if jobs := store.Jobs(); len(jobs) != 0 {
		t.Errorf("console commands became a job: %+v", jobs)
	}
}

func TestProgramEnd(t *testing.T) {
	cases := map[string]bool{
		"M2":              true,
		"m30":             true,
		"M02":             true,
		"G0 X0 Y0 M30":    true,
		"M5 M2 (done)":    true,
		"M3 S1000":        false,
		"M20":             false,
		"M200":            false,
		"G1 X2 (M2 here)": false,
		"; M30":           false,
		"M300":            false,
	}
	for line, want := range cases {
		if got := isProgramEnd(line); got != want {
			t.Errorf("isProgramEnd(%q) = %v, want %v", line, got, want)
		}
	}
}

// fakeCarbide plays Carbide Motion's side of the remote access protocol.
func fakeCarbide(t *testing.T, state string, got chan<- delivery) string {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { l.Close() })
	go func() {
		conn, err := l.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		fmt.Fprintf(conn, "STATE: %s\n", state)
		r := bufio.NewReader(conn)
		header, err := r.ReadString('\n')
		if err != nil {
			return
		}
		header = strings.TrimSuffix(strings.TrimPrefix(header, "GCODE: "), "\n")
		i := strings.LastIndex(header, ":")
		var size int
		fmt.Sscanf(header[i+1:], "%d", &size)
		body := make([]byte, size+1)
		if _, err := io.ReadFull(r, body); err != nil {
			return
		}
		got <- delivery{header[:i], string(body[:size])}
		fmt.Fprint(conn, "GCODE_ACK")
	}()
	return l.Addr().String()
}

func TestSendCarbide(t *testing.T) {
	got := make(chan delivery, 1)
	addr := fakeCarbide(t, "init", got)
	body := "G21\nG0 X1\nM2\n"
	if err := SendCarbide(addr, "job.nc", []byte(body), 5*time.Second); err != nil {
		t.Fatal(err)
	}
	d := <-got
	if d.name != "job.nc" || d.body != body {
		t.Errorf("Carbide Motion received %+v", d)
	}
}

func TestSendCarbideNotReady(t *testing.T) {
	addr := fakeCarbide(t, "running", make(chan delivery, 1))
	err := SendCarbide(addr, "job.nc", []byte("G21\n"), 5*time.Second)
	if err == nil || !strings.Contains(err.Error(), "not ready") {
		t.Errorf("err = %v, want a not-ready error", err)
	}
}

func TestAllowlist(t *testing.T) {
	private, _ := ParseAllowlist("private")
	for addr, want := range map[string]bool{"192.168.1.20:5000": true, "127.0.0.1:1": true, "8.8.8.8:53": false} {
		tcp, _ := net.ResolveTCPAddr("tcp", addr)
		if got := private.Permits(tcp); got != want {
			t.Errorf("private.Permits(%s) = %v, want %v", addr, got, want)
		}
	}
	if _, err := ParseAllowlist("10.0.0.5, 192.168.4.0/24"); err != nil {
		t.Error(err)
	}
	if _, err := ParseAllowlist("nonsense"); err == nil {
		t.Error("bad allow list accepted")
	}
}

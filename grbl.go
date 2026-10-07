package main

import (
	"bufio"
	"errors"
	"fmt"
	"log"
	"net"
	"regexp"
	"strings"
	"sync/atomic"
	"time"
)

const maxLineLen = 512

// Server pretends to be a GRBL 1.1 controller on a TCP port. It answers what
// a sender asks, acknowledges every line, and collects the G-code into jobs.
type Server struct {
	Store    *Store
	Allow    Allowlist
	Idle     time.Duration // silence that ends a program with no M2/M30
	MinLines int           // shorter programs are console commands, not jobs
	Travel   [3]float64    // machine travel reported in $130-$132
	Trace    bool          // log every line received

	clients  atomic.Int32
	lastSeen atomic.Int64 // unix seconds of the last byte from a sender
}

// Clients reports how many senders are connected.
func (s *Server) Clients() int { return int(s.clients.Load()) }

// Serve accepts sender connections until the listener fails.
func (s *Server) Serve(l net.Listener) error {
	for {
		conn, err := l.Accept()
		if err != nil {
			return err
		}
		if !s.Allow.Permits(conn.RemoteAddr()) {
			log.Printf("refused connection from %s (not in the allow list)", conn.RemoteAddr())
			conn.Close()
			continue
		}
		go s.handle(conn)
	}
}

type session struct {
	srv    *Server
	w      *bufio.Writer
	remote string
	inJob  bool
	lines  []string
}

func (s *Server) handle(conn net.Conn) {
	defer conn.Close()
	remote := conn.RemoteAddr().String()
	if host, _, err := net.SplitHostPort(remote); err == nil {
		remote = host
	}
	log.Printf("sender connected from %s", remote)
	s.clients.Add(1)
	defer s.clients.Add(-1)

	r := bufio.NewReaderSize(conn, 64<<10)
	w := bufio.NewWriterSize(conn, 16<<10)
	sess := &session{srv: s, w: w, remote: remote}
	sess.greet()

	var line []byte
	for {
		if r.Buffered() == 0 {
			if err := w.Flush(); err != nil {
				break
			}
			// Only a program in progress can time out.
			if sess.inJob {
				conn.SetReadDeadline(time.Now().Add(s.Idle))
			} else {
				conn.SetReadDeadline(time.Time{})
			}
		}
		b, err := r.ReadByte()
		if err != nil {
			var ne net.Error
			if sess.inJob && errors.As(err, &ne) && ne.Timeout() {
				sess.finish(false, "the sender went quiet for "+s.Idle.String())
				continue
			}
			break
		}
		s.lastSeen.Store(time.Now().Unix())
		switch {
		case b == '?':
			sess.status()
		case b == 0x18: // soft reset
			line = line[:0]
			sess.reset()
		case b == '!' || b == '~' || b >= 0x80:
			// Feed hold, resume, jog cancel and overrides have nothing to act on.
		case b == '\n' || b == '\r':
			if t := strings.TrimSpace(string(line)); t != "" {
				sess.line(t)
			}
			line = line[:0]
		default:
			if len(line) < maxLineLen {
				line = append(line, b)
			}
		}
	}
	if sess.inJob {
		sess.finish(false, "the sender disconnected")
	}
	w.Flush()
	log.Printf("sender %s disconnected", remote)
}

func (c *session) reply(format string, args ...any) {
	fmt.Fprintf(c.w, format+"\r\n", args...)
}

func (c *session) greet() {
	c.reply("")
	c.reply("Grbl 1.1h ['$' for help]")
}

// status always reports Idle at the origin. The bridge never moves anything,
// and a sender that saw Run or Alarm could refuse to start a job.
func (c *session) status() {
	c.reply("<Idle|MPos:0.000,0.000,0.000|Bf:15,128|FS:0,0|WCO:0.000,0.000,0.000>")
}

func (c *session) reset() {
	if c.inJob {
		log.Printf("sender %s reset mid-program; dropped %d lines", c.remote, len(c.lines))
		c.inJob, c.lines = false, nil
	}
	c.greet()
}

func (c *session) line(text string) {
	if c.srv.Trace {
		log.Printf("recv %s: %s", c.remote, text)
	}
	if strings.HasPrefix(text, "$") {
		c.dollar(text)
		return
	}
	if !c.inJob {
		c.inJob, c.lines = true, nil
	}
	c.lines = append(c.lines, text)
	c.reply("ok")
	if isProgramEnd(text) {
		c.finish(true, "")
	}
}

func (c *session) finish(complete bool, note string) {
	lines := c.lines
	c.inJob, c.lines = false, nil
	if len(lines) < c.srv.MinLines {
		log.Printf("ignored %d line(s) from %s: too short to be a job", len(lines), c.remote)
		return
	}
	c.srv.Store.Add(c.remote, lines, complete, note)
}

// dollar answers GRBL system commands. None of them belong in a job.
func (c *session) dollar(text string) {
	if !c.srv.Trace {
		log.Printf("sender %s: %s", c.remote, text)
	}
	cmd := strings.ToUpper(text)
	switch {
	case cmd == "$":
		c.reply("[HLP:$$ $# $G $I $N $x=val $Nx=line $J=line $SLP $C $X $H ~ ! ? ctrl-x]")
	case cmd == "$$":
		for _, setting := range c.srv.settings() {
			c.reply("%s", setting)
		}
	case cmd == "$I":
		c.reply("[VER:1.1h.20190825:]")
		c.reply("[OPT:V,15,128]")
	case cmd == "$G":
		c.reply("[GC:G0 G54 G17 G21 G90 G94 M5 M9 T0 F0 S0]")
	case cmd == "$#":
		for _, name := range []string{"G54", "G55", "G56", "G57", "G58", "G59", "G28", "G30", "G92"} {
			c.reply("[%s:0.000,0.000,0.000]", name)
		}
		c.reply("[TLO:0.000]")
		c.reply("[PRB:0.000,0.000,0.000:0]")
	case cmd == "$N":
		c.reply("$N0=")
		c.reply("$N1=")
	case cmd == "$X":
		c.reply("[MSG:Caution: Unlocked]")
	}
	// Homing, jogging, setting writes and anything unknown: accept and ignore.
	c.reply("ok")
}

func (s *Server) settings() []string {
	return []string{
		"$0=10", "$1=255", "$2=0", "$3=0", "$4=0", "$5=0", "$6=0",
		"$10=1", "$11=0.010", "$12=0.002", "$13=0",
		"$20=0", "$21=0", "$22=1", "$23=0", "$24=100.000", "$25=2000.000", "$26=25", "$27=5.000",
		"$30=24000", "$31=0", "$32=0",
		"$100=40.000", "$101=40.000", "$102=200.000",
		"$110=10000.000", "$111=10000.000", "$112=1000.000",
		"$120=500.000", "$121=500.000", "$122=270.000",
		fmt.Sprintf("$130=%.3f", s.Travel[0]),
		fmt.Sprintf("$131=%.3f", s.Travel[1]),
		fmt.Sprintf("$132=%.3f", s.Travel[2]),
	}
}

var (
	commentRe    = regexp.MustCompile(`\([^)]*\)|;.*$`)
	programEndRe = regexp.MustCompile(`M0*(2|30)([^0-9.]|$)`)
)

// isProgramEnd reports whether a G-code line contains M2 or M30.
func isProgramEnd(line string) bool {
	code := commentRe.ReplaceAllString(line, "")
	code = strings.ToUpper(strings.Join(strings.Fields(code), ""))
	return programEndRe.MatchString(code)
}

// Allowlist limits which addresses may connect. Nil permits everyone.
type Allowlist []*net.IPNet

var privateNets = "127.0.0.0/8,10.0.0.0/8,172.16.0.0/12,192.168.0.0/16,169.254.0.0/16,::1/128,fc00::/7,fe80::/10"

// ParseAllowlist reads "private", "any", or a comma-separated list of
// addresses and CIDR ranges.
func ParseAllowlist(spec string) (Allowlist, error) {
	switch strings.ToLower(strings.TrimSpace(spec)) {
	case "any":
		return nil, nil
	case "", "private":
		spec = privateNets
	}
	var list Allowlist
	for _, item := range strings.Split(spec, ",") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		if !strings.Contains(item, "/") {
			ip := net.ParseIP(item)
			if ip == nil {
				return nil, fmt.Errorf("bad address %q in allow list", item)
			}
			if ip.To4() != nil {
				item += "/32"
			} else {
				item += "/128"
			}
		}
		_, ipnet, err := net.ParseCIDR(item)
		if err != nil {
			return nil, fmt.Errorf("bad range %q in allow list", item)
		}
		list = append(list, ipnet)
	}
	return list, nil
}

// Permits reports whether addr may connect.
func (a Allowlist) Permits(addr net.Addr) bool {
	if a == nil {
		return true
	}
	host, _, err := net.SplitHostPort(addr.String())
	if err != nil {
		host = addr.String()
	}
	if i := strings.IndexByte(host, '%'); i >= 0 {
		host = host[:i]
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	for _, n := range a {
		if n.Contains(ip) {
			return true
		}
	}
	return false
}

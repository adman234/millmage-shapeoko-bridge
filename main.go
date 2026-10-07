// millmage-bridge lets MillMage send a job over the network to a Shapeoko
// whose sender (Carbide Motion) stays in control of the machine. MillMage
// connects to it as a GRBL network device; the bridge captures the program
// and hands it to the sender.
package main

import (
	"bufio"
	"flag"
	"fmt"
	"log"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

var version = "dev"

func main() {
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "version", "-version", "--version":
			fmt.Println(version)
			return
		case "send":
			os.Exit(runSend(os.Args[2:]))
		}
	}
	os.Exit(runServe(os.Args[1:]))
}

// runSend delivers one file to Carbide Motion and exits. It is the quick
// check that the installed Carbide Motion build accepts files this way.
func runSend(args []string) int {
	fs := flag.NewFlagSet("send", flag.ExitOnError)
	addr := fs.String("addr", "127.0.0.1:6280", "address of the computer running Carbide Motion")
	fs.Usage = func() {
		fmt.Fprintln(os.Stderr, "usage: millmage-bridge send [-addr host[:port]] file.nc")
		fs.PrintDefaults()
	}
	fs.Parse(args)
	if fs.NArg() != 1 {
		fs.Usage()
		return 2
	}
	body, err := os.ReadFile(fs.Arg(0))
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	name := strings.NewReplacer(":", "_", " ", "_").Replace(filepath.Base(fs.Arg(0)))
	if err := SendCarbide(*addr, name, body, 60*time.Second); err != nil {
		fmt.Fprintln(os.Stderr, "failed:", err)
		return 1
	}
	fmt.Printf("Carbide Motion acknowledged %d bytes (%d lines). Compare that with what it shows before cutting.\n",
		len(body), strings.Count(string(body), "\n"))
	return 0
}

func runServe(args []string) int {
	fs := flag.NewFlagSet("millmage-bridge", flag.ExitOnError)
	var (
		configPath  = fs.String("config", "", "settings file (default: bridge.conf beside the program, if present)")
		listen      = fs.String("listen", ":23", "address MillMage connects to")
		httpAddr    = fs.String("http", ":8080", "address of the status page; \"off\" disables it")
		backend     = fs.String("backend", "carbide", "where jobs go: \"carbide\" (Carbide Motion) or \"folder\" (only save to jobs-dir)")
		carbideAddr = fs.String("carbide-addr", "127.0.0.1:6280", "address of Carbide Motion's remote access listener")
		jobsDir     = fs.String("jobs-dir", "", "folder where received jobs are saved (default: jobs beside the program)")
		keep        = fs.Int("keep", 50, "how many jobs to keep on disk; 0 keeps all")
		idle        = fs.Duration("idle-timeout", 5*time.Second, "silence that ends a program with no M2/M30")
		minLines    = fs.Int("min-lines", 5, "programs shorter than this are treated as console commands and ignored")
		requireEnd  = fs.Bool("require-end", true, "hold jobs that do not end with M2/M30 instead of sending them")
		allow       = fs.String("allow", "private", "who may connect: \"private\" (LAN addresses), \"any\", or a list of addresses and CIDR ranges")
		travel      = fs.String("travel", "838,444,100", "machine travel X,Y,Z in mm, reported to MillMage")
		logFile     = fs.String("log-file", "", "write the log to this file instead of the console")
		trace       = fs.Bool("trace", false, "log every line MillMage sends")
	)
	fs.Parse(args)

	path := *configPath
	if path == "" {
		if p := filepath.Join(exeDir(), "bridge.conf"); fileExists(p) {
			path = p
		}
	}
	if path != "" {
		if err := applyConfig(fs, path); err != nil {
			fmt.Fprintln(os.Stderr, "config:", err)
			return 2
		}
	}

	if *logFile != "" {
		if info, err := os.Stat(*logFile); err == nil && info.Size() > 5<<20 {
			os.Rename(*logFile, *logFile+".old")
		}
		f, err := os.OpenFile(*logFile, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "log file:", err)
			return 2
		}
		log.SetOutput(f)
	}

	allowlist, err := ParseAllowlist(*allow)
	if err != nil {
		log.Print(err)
		return 2
	}
	travelMM, err := parseTravel(*travel)
	if err != nil {
		log.Print(err)
		return 2
	}
	if *jobsDir == "" {
		*jobsDir = filepath.Join(exeDir(), "jobs")
	}

	store := &Store{Dir: *jobsDir, Keep: *keep, RequireEnd: *requireEnd, RetryDelay: 3 * time.Second}
	target := "the folder " + *jobsDir
	switch *backend {
	case "carbide":
		addr := *carbideAddr
		store.Deliver = func(name string, body []byte) error {
			return SendCarbide(addr, name, body, 60*time.Second)
		}
		target = "Carbide Motion at " + addr
	case "folder":
	default:
		log.Printf("unknown backend %q (use carbide or folder)", *backend)
		return 2
	}
	if err := store.Load(); err != nil {
		log.Printf("jobs folder: %v", err)
		return 1
	}

	srv := &Server{Store: store, Allow: allowlist, Idle: *idle, MinLines: *minLines, Travel: travelMM, Trace: *trace}
	l, err := net.Listen("tcp", *listen)
	if err != nil {
		log.Printf("cannot listen on %s: %v", *listen, err)
		return 1
	}
	log.Printf("millmage-bridge %s: waiting for MillMage on %s, sending jobs to %s", version, *listen, target)

	if *httpAddr != "" && *httpAddr != "off" {
		web := &Web{Store: store, Server: srv, Allow: allowlist, Backend: *backend, Target: target, Listen: strings.TrimPrefix(*listen, ":")}
		go func() {
			log.Printf("status page on %s", *httpAddr)
			if err := http.ListenAndServe(*httpAddr, web.Handler()); err != nil {
				log.Printf("status page unavailable: %v", err)
			}
		}()
	}

	log.Print(srv.Serve(l))
	return 1
}

// applyConfig reads "name = value" lines and applies them to flags that were
// not given on the command line.
func applyConfig(fs *flag.FlagSet, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	given := map[string]bool{}
	fs.Visit(func(fl *flag.Flag) { given[fl.Name] = true })
	sc := bufio.NewScanner(f)
	for n := 1; sc.Scan(); n++ {
		line := strings.TrimSpace(strings.TrimPrefix(sc.Text(), string(rune(0xFEFF))))
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		name, value, ok := strings.Cut(line, "=")
		if !ok {
			return fmt.Errorf("%s line %d: expected name = value", path, n)
		}
		name, value = strings.TrimSpace(name), strings.TrimSpace(value)
		if name == "config" || given[name] {
			continue
		}
		if err := fs.Set(name, value); err != nil {
			return fmt.Errorf("%s line %d: %v", path, n, err)
		}
	}
	return sc.Err()
}

func parseTravel(spec string) ([3]float64, error) {
	var out [3]float64
	parts := strings.Split(spec, ",")
	if len(parts) != 3 {
		return out, fmt.Errorf("travel must be X,Y,Z in mm, got %q", spec)
	}
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil || v <= 0 {
			return out, fmt.Errorf("travel must be X,Y,Z in mm, got %q", spec)
		}
		out[i] = v
	}
	return out, nil
}

func exeDir() string {
	exe, err := os.Executable()
	if err != nil {
		return "."
	}
	return filepath.Dir(exe)
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	return err == nil && !info.IsDir()
}

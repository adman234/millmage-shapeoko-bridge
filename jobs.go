package main

import (
	"fmt"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const (
	jobPrefix = "millmage-"
	jobExt    = ".nc"
)

// Job statuses.
const (
	StatusSending   = "sending"
	StatusDelivered = "delivered"
	StatusSaved     = "saved"
	StatusHeld      = "held"
	StatusFailed    = "failed"
	StatusOnDisk    = "on disk"
)

// Job is one program captured from MillMage.
type Job struct {
	ID       string    `json:"id"`
	File     string    `json:"file"`
	Received time.Time `json:"received"`
	Source   string    `json:"source"`
	Lines    int       `json:"lines"`
	Bytes    int       `json:"bytes"`
	Status   string    `json:"status"`
	Detail   string    `json:"detail"`
}

// Store keeps captured jobs on disk and hands them to the sender.
type Store struct {
	Dir        string
	Keep       int  // newest jobs to keep on disk; 0 keeps everything
	RequireEnd bool // hold jobs that did not end with M2/M30
	// Deliver passes a job to the sender. Nil means saving to Dir is the
	// whole handoff (folder backend).
	Deliver func(name string, body []byte) error
	// RetryDelay is the pause between delivery attempts.
	RetryDelay time.Duration

	mu     sync.Mutex
	jobs   []*Job // newest first
	sendMu sync.Mutex
}

// Load creates the job folder and lists jobs left from earlier runs.
func (s *Store) Load() error {
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return err
	}
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return err
	}
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasPrefix(name, jobPrefix) || !strings.HasSuffix(name, jobExt) {
			continue
		}
		info, err := e.Info()
		if err != nil {
			continue
		}
		s.jobs = append(s.jobs, &Job{
			ID:       strings.TrimSuffix(strings.TrimPrefix(name, jobPrefix), jobExt),
			File:     name,
			Received: info.ModTime(),
			Bytes:    int(info.Size()),
			Status:   StatusOnDisk,
			Detail:   "Received before the bridge last restarted.",
		})
	}
	sort.Slice(s.jobs, func(i, j int) bool { return s.jobs[i].Received.After(s.jobs[j].Received) })
	return nil
}

// Add saves a captured program and starts its handoff. complete reports
// whether the program ended with M2 or M30; note says how it ended otherwise.
func (s *Store) Add(source string, lines []string, complete bool, note string) (*Job, error) {
	body := []byte(strings.Join(lines, "\n") + "\n")
	now := time.Now()

	s.mu.Lock()
	id := now.Format("20060102-150405")
	for n := 2; s.find(id) != nil; n++ {
		id = fmt.Sprintf("%s-%d", now.Format("20060102-150405"), n)
	}
	job := &Job{
		ID:       id,
		File:     jobPrefix + id + jobExt,
		Received: now,
		Source:   source,
		Lines:    len(lines),
		Bytes:    len(body),
		Status:   StatusSending,
	}
	s.jobs = append([]*Job{job}, s.jobs...)
	s.mu.Unlock()

	if err := os.WriteFile(filepath.Join(s.Dir, job.File), body, 0o644); err != nil {
		s.set(job, StatusFailed, "Could not save the job: "+err.Error())
		return job, err
	}
	log.Printf("job %s: received %d lines, %d bytes from %s", job.ID, job.Lines, job.Bytes, source)
	s.prune()

	if !complete && s.RequireEnd {
		s.set(job, StatusHeld, fmt.Sprintf("The program did not end with M2 or M30 (%s), so it may be cut short. Not sent.", note))
		return job, nil
	}
	go s.dispatch(job, body)
	return job, nil
}

// Resend delivers a stored job again, including held ones.
func (s *Store) Resend(id string) error {
	s.mu.Lock()
	job := s.find(id)
	s.mu.Unlock()
	if job == nil {
		return fmt.Errorf("no job %q", id)
	}
	body, err := os.ReadFile(filepath.Join(s.Dir, job.File))
	if err != nil {
		return err
	}
	s.set(job, StatusSending, "")
	go s.dispatch(job, body)
	return nil
}

func (s *Store) dispatch(job *Job, body []byte) {
	if s.Deliver == nil {
		s.set(job, StatusSaved, "Saved to "+filepath.Join(s.Dir, job.File))
		return
	}
	s.sendMu.Lock()
	defer s.sendMu.Unlock()
	var err error
	for attempt := 1; attempt <= 3; attempt++ {
		if err = s.Deliver(job.File, body); err == nil {
			s.set(job, StatusDelivered, fmt.Sprintf("Carbide Motion acknowledged %d bytes.", len(body)))
			return
		}
		log.Printf("job %s: delivery attempt %d failed: %v", job.ID, attempt, err)
		if attempt < 3 {
			time.Sleep(s.RetryDelay)
		}
	}
	s.set(job, StatusFailed, err.Error())
}

// Jobs returns a copy of the job list, newest first.
func (s *Store) Jobs() []Job {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Job, len(s.jobs))
	for i, j := range s.jobs {
		out[i] = *j
	}
	return out
}

// Path returns the file for a job, or "" if there is no such job.
func (s *Store) Path(id string) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if job := s.find(id); job != nil {
		return filepath.Join(s.Dir, job.File)
	}
	return ""
}

func (s *Store) set(job *Job, status, detail string) {
	s.mu.Lock()
	job.Status, job.Detail = status, detail
	s.mu.Unlock()
	if status != StatusSending {
		log.Printf("job %s: %s. %s", job.ID, status, detail)
	}
}

// find needs s.mu held.
func (s *Store) find(id string) *Job {
	for _, j := range s.jobs {
		if j.ID == id {
			return j
		}
	}
	return nil
}

func (s *Store) prune() {
	if s.Keep <= 0 {
		return
	}
	s.mu.Lock()
	var old []*Job
	if len(s.jobs) > s.Keep {
		old = s.jobs[s.Keep:]
		s.jobs = s.jobs[:s.Keep]
	}
	s.mu.Unlock()
	for _, j := range old {
		os.Remove(filepath.Join(s.Dir, j.File))
	}
}

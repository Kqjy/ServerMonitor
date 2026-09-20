package ipban

import (
	"bufio"
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

const (
	sourceStarting     = "starting"
	sourceOK           = "ok"
	sourceNoPermission = "no_permission"
	sourceUnavailable  = "unavailable"
	sourceError        = "error"
	sourceRetryIdle    = 5 * time.Minute
	sourceRetryMax     = time.Minute
)

var (
	journalctlCandidates   = []string{"/usr/bin/journalctl", "/bin/journalctl"}
	authLogCandidates      = []string{"/var/log/auth.log", "/var/log/secure", "/var/log/messages"}
	journalPermissionHints = []string{
		"insufficient permissions",
		"not seeing messages from other users",
		"Permission denied",
		"No journal files were opened",
	}
)

type SourceStatus struct {
	Name    string
	State   string
	Message string
}

type sshdSource struct {
	logger     *slog.Logger
	emit       func(Failure)
	now        func() time.Time
	aggressive atomic.Bool
	mu         sync.Mutex
	status     SourceStatus
}

func newSSHDSource(logger *slog.Logger, emit func(Failure), now func() time.Time) *sshdSource {
	return &sshdSource{logger: logger, emit: emit, now: now, status: SourceStatus{Name: "sshd", State: sourceStarting}}
}

func (s *sshdSource) Status() SourceStatus {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.status
}

func (s *sshdSource) setStatus(state, message string) {
	s.mu.Lock()
	changed := s.status.State != state || s.status.Message != message
	s.status = SourceStatus{Name: "sshd", State: state, Message: message}
	s.mu.Unlock()
	if changed {
		if state == sourceOK {
			s.logger.Info("ipban sshd source running", "via", message)
		} else {
			s.logger.Warn("ipban sshd source unavailable", "state", state, "detail", message)
		}
	}
}

func (s *sshdSource) Run(ctx context.Context) {
	backoff := time.Second
	for {
		state, message := s.runOnce(ctx)
		if ctx.Err() != nil {
			return
		}
		s.setStatus(state, message)
		wait := backoff
		switch state {
		case sourceNoPermission, sourceUnavailable:
			wait = sourceRetryIdle
		default:
			backoff *= 2
			if backoff > sourceRetryMax {
				backoff = sourceRetryMax
			}
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
	}
}

func (s *sshdSource) runOnce(ctx context.Context) (string, string) {
	bin := trustedBinary(journalctlCandidates)
	if bin == "" {
		return s.runFile(ctx)
	}
	state, message := s.runJournal(ctx, bin)
	if ctx.Err() != nil {
		return sourceOK, ""
	}
	if state != sourceNoPermission && state != sourceUnavailable {
		return state, message
	}
	fileState, fileMessage := s.runFile(ctx)
	if fileState == sourceNoPermission || fileState == sourceUnavailable {
		return state, message + "; " + fileMessage
	}
	return fileState, fileMessage
}

func (s *sshdSource) runJournal(ctx context.Context, bin string) (string, string) {
	cctx, cancel := context.WithCancel(ctx)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, "-f", "-n", "0", "-q", "-o", "json", "-t", "sshd", "-t", "sshd-session", "--no-pager")
	cmd.Env = []string{"PATH=/usr/sbin:/usr/bin:/sbin:/bin", "LC_ALL=C"}
	cmd.Cancel = func() error { return cmd.Process.Signal(os.Interrupt) }
	cmd.WaitDelay = 5 * time.Second
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return sourceError, "journalctl stdout: " + err.Error()
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return sourceError, "journalctl stderr: " + err.Error()
	}
	if err := cmd.Start(); err != nil {
		return sourceUnavailable, "journalctl failed to start: " + err.Error()
	}
	var denied atomic.Bool
	var errMu sync.Mutex
	var errText strings.Builder
	stderrDone := make(chan struct{})
	go func() {
		defer close(stderrDone)
		sc := bufio.NewScanner(stderr)
		for sc.Scan() {
			line := sc.Text()
			errMu.Lock()
			if errText.Len() < 2048 {
				if errText.Len() > 0 {
					errText.WriteString(" | ")
				}
				errText.WriteString(line)
			}
			errMu.Unlock()
			if isJournalPermissionHint(line) {
				denied.Store(true)
				cancel()
			}
		}
	}()
	s.setStatus(sourceOK, "journald via "+bin)
	sc := bufio.NewScanner(stdout)
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)
	for sc.Scan() {
		s.handleJournalLine(sc.Bytes())
	}
	waitErr := cmd.Wait()
	<-stderrDone
	errMu.Lock()
	detail := strings.TrimSpace(errText.String())
	errMu.Unlock()
	if denied.Load() {
		return sourceNoPermission, "journalctl: " + detail
	}
	if ctx.Err() != nil {
		return sourceOK, ""
	}
	if detail == "" {
		detail = fmt.Sprint(waitErr)
	}
	return sourceError, "journalctl exited: " + detail
}

func isJournalPermissionHint(line string) bool {
	for _, hint := range journalPermissionHints {
		if strings.Contains(line, hint) {
			return true
		}
	}
	return false
}

func (s *sshdSource) handleJournalLine(line []byte) {
	var entry struct {
		Message    json.RawMessage `json:"MESSAGE"`
		Timestamp  string          `json:"__REALTIME_TIMESTAMP"`
		Identifier string          `json:"SYSLOG_IDENTIFIER"`
	}
	if err := json.Unmarshal(line, &entry); err != nil {
		return
	}
	if entry.Identifier != "sshd" && entry.Identifier != "sshd-session" {
		return
	}
	var message string
	if err := json.Unmarshal(entry.Message, &message); err != nil {
		return
	}
	at := s.now()
	if micros, err := strconv.ParseInt(entry.Timestamp, 10, 64); err == nil && micros > 0 {
		at = time.UnixMicro(micros)
	}
	s.handleMessage(message, at)
}

func (s *sshdSource) handleMessage(message string, at time.Time) {
	match, ok := ParseSSHD(message, s.aggressive.Load())
	if !ok {
		return
	}
	s.emit(Failure{Time: at, IP: match.IP, User: match.User, Source: "sshd"})
}

func authLogPaths() []string {
	root := strings.TrimRight(strings.TrimSpace(os.Getenv("SM_HOST_FS_ROOT")), "/")
	if root == "" {
		return authLogCandidates
	}
	out := make([]string, 0, len(authLogCandidates))
	for _, p := range authLogCandidates {
		out = append(out, root+p)
	}
	return out
}

func (s *sshdSource) runFile(ctx context.Context) (string, string) {
	var notes []string
	permission := false
	candidates := authLogPaths()
	for _, path := range candidates {
		f, err := os.Open(path)
		if err != nil {
			switch {
			case errors.Is(err, os.ErrPermission):
				permission = true
				notes = append(notes, path+": permission denied")
			case errors.Is(err, os.ErrNotExist):
			default:
				notes = append(notes, path+": "+err.Error())
			}
			continue
		}
		return s.tailFile(ctx, path, f)
	}
	detail := "no readable auth log (tried " + strings.Join(candidates, ", ") + ")"
	if len(notes) > 0 {
		detail += ": " + strings.Join(notes, "; ")
	}
	if permission {
		return sourceNoPermission, detail
	}
	return sourceUnavailable, detail
}

func (s *sshdSource) tailFile(ctx context.Context, path string, f *os.File) (string, string) {
	defer func() { _ = f.Close() }()
	latest, seenAtLatest, pos, err := seedFileCursor(f, s.now())
	if err != nil {
		return sourceError, path + ": " + err.Error()
	}
	s.setStatus(sourceOK, "file tail of "+path)
	buf := make([]byte, 64*1024)
	var pending []byte
	lastCheck := s.now()
	for {
		n, err := f.Read(buf)
		if n > 0 {
			pos += int64(n)
			pending = append(pending, buf[:n]...)
			for {
				i := bytes.IndexByte(pending, '\n')
				if i < 0 {
					break
				}
				line := string(pending[:i])
				pending = pending[i+1:]
				if message, at, ok := ParseSyslogLine(line, s.now()); ok && acceptFileLine(line, at, &latest, &seenAtLatest) {
					s.handleMessage(message, at)
				}
			}
			if len(pending) > 1<<20 {
				pending = pending[:0]
			}
			continue
		}
		if err != nil && !errors.Is(err, io.EOF) {
			return sourceError, path + ": " + err.Error()
		}
		select {
		case <-ctx.Done():
			return sourceOK, ""
		case <-time.After(500 * time.Millisecond):
		}
		now := s.now()
		if now.Sub(lastCheck) < 2*time.Second {
			continue
		}
		lastCheck = now
		fi, statErr := os.Stat(path)
		cur, curErr := f.Stat()
		if statErr != nil || curErr != nil {
			continue
		}
		if os.SameFile(fi, cur) && fi.Size() >= pos {
			continue
		}
		nf, err := os.Open(path)
		if err != nil {
			return sourceError, path + ": reopen after rotation: " + err.Error()
		}
		_ = f.Close()
		f = nf
		pos = 0
		pending = pending[:0]
	}
}

const fileSeedBytes = 2 << 20

func seedFileCursor(f *os.File, now time.Time) (time.Time, map[[sha256.Size]byte]struct{}, int64, error) {
	end, err := f.Seek(0, io.SeekEnd)
	if err != nil {
		return time.Time{}, nil, 0, err
	}
	start := end - fileSeedBytes
	if start < 0 {
		start = 0
	}
	if _, err := f.Seek(start, io.SeekStart); err != nil {
		return time.Time{}, nil, 0, err
	}
	data, err := io.ReadAll(io.LimitReader(f, fileSeedBytes))
	if err != nil {
		return time.Time{}, nil, 0, err
	}
	if start > 0 {
		if i := bytes.IndexByte(data, '\n'); i >= 0 {
			data = data[i+1:]
		} else {
			data = nil
		}
	}
	var latest time.Time
	seen := make(map[[sha256.Size]byte]struct{})
	for _, raw := range bytes.Split(data, []byte{'\n'}) {
		line := string(raw)
		_, at, ok := ParseSyslogLine(line, now)
		if ok {
			acceptFileLine(line, at, &latest, &seen)
		}
	}
	if _, err := f.Seek(end, io.SeekStart); err != nil {
		return time.Time{}, nil, 0, err
	}
	return latest, seen, end, nil
}

func acceptFileLine(line string, at time.Time, latest *time.Time, seen *map[[sha256.Size]byte]struct{}) bool {
	digest := sha256.Sum256([]byte(line))
	switch {
	case at.Before(*latest):
		return false
	case at.Equal(*latest):
		if _, duplicate := (*seen)[digest]; duplicate {
			return false
		}
		(*seen)[digest] = struct{}{}
		return true
	default:
		*latest = at
		*seen = map[[sha256.Size]byte]struct{}{digest: {}}
		return true
	}
}

func trustedBinary(candidates []string) string {
	for _, p := range candidates {
		fi, err := os.Lstat(p)
		if err != nil {
			continue
		}
		if fi.Mode()&os.ModeSymlink != 0 || !fi.Mode().IsRegular() {
			continue
		}
		return p
	}
	return ""
}

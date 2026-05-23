package collectors

import (
	"bytes"
	"context"
	"os"
	"strconv"
)

func scanProcCounts(ctx context.Context) (procCountSnapshot, error) {
	var snap procCountSnapshot
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return snap, err
	}
	for _, e := range entries {
		if ctx.Err() != nil {
			return snap, ctx.Err()
		}
		name := e.Name()
		if !isAllDigits(name) {
			continue
		}
		snap.pids++
		data, err := os.ReadFile("/proc/" + name + "/stat")
		if err != nil {
			continue
		}
		state, nThreads, ok := parseProcStat(data)
		if !ok {
			continue
		}
		switch state {
		case 'R', 'D':
			snap.running++
		case 'S', 'I':
			snap.sleep++
		case 'Z':
			snap.zombie++
		case 'T', 't':
			snap.stopped++
		}
		snap.threads += nThreads
	}
	return snap, nil
}

func isAllDigits(s string) bool {
	if s == "" {
		return false
	}
	for i := 0; i < len(s); i++ {
		if s[i] < '0' || s[i] > '9' {
			return false
		}
	}
	return true
}

func parseProcStat(data []byte) (state byte, nThreads int, ok bool) {
	closeIdx := bytes.LastIndexByte(data, ')')
	if closeIdx < 0 || closeIdx+2 >= len(data) {
		return 0, 0, false
	}
	fields := bytes.Fields(data[closeIdx+1:])
	if len(fields) < 18 {
		return 0, 0, false
	}
	if len(fields[0]) == 0 {
		return 0, 0, false
	}
	n, err := strconv.Atoi(string(fields[17]))
	if err != nil {
		return 0, 0, false
	}
	return fields[0][0], n, true
}

package agentdist

import (
	"embed"
	"errors"
	"io/fs"
	"strings"
)

//go:embed all:binaries
var binariesFS embed.FS

type Platform struct {
	ID       string
	OS       string
	Arch     string
	Label    string
	Filename string
	Size     int64
}

var platforms = []Platform{
	{ID: "linux-amd64", OS: "linux", Arch: "amd64", Label: "Linux (x86_64)", Filename: "sm-agent-linux-amd64"},
	{ID: "linux-arm64", OS: "linux", Arch: "arm64", Label: "Linux (ARM64)", Filename: "sm-agent-linux-arm64"},
	{ID: "windows-amd64", OS: "windows", Arch: "amd64", Label: "Windows (x86_64)", Filename: "sm-agent-windows-amd64.exe"},
	{ID: "darwin-amd64", OS: "darwin", Arch: "amd64", Label: "macOS (Intel)", Filename: "sm-agent-darwin-amd64"},
	{ID: "darwin-arm64", OS: "darwin", Arch: "arm64", Label: "macOS (Apple Silicon)", Filename: "sm-agent-darwin-arm64"},
}

var ErrUnknownPlatform = errors.New("unknown platform")

func Available() []Platform {
	out := make([]Platform, 0, len(platforms))
	for _, p := range platforms {
		info, err := fs.Stat(binariesFS, "binaries/"+p.Filename)
		if err != nil {
			continue
		}
		p.Size = info.Size()
		out = append(out, p)
	}
	return out
}

func Lookup(id string) (Platform, bool) {
	for _, p := range platforms {
		if p.ID == id {
			if info, err := fs.Stat(binariesFS, "binaries/"+p.Filename); err == nil {
				p.Size = info.Size()
				return p, true
			}
			return Platform{}, false
		}
	}
	return Platform{}, false
}

func Open(id string) (fs.File, Platform, error) {
	p, ok := Lookup(id)
	if !ok {
		return nil, Platform{}, ErrUnknownPlatform
	}
	f, err := binariesFS.Open("binaries/" + p.Filename)
	if err != nil {
		return nil, Platform{}, err
	}
	return f, p, nil
}

func PlatformFromUserAgent(ua string) string {
	low := strings.ToLower(ua)
	switch {
	case strings.Contains(low, "windows"):
		return "windows-amd64"
	case strings.Contains(low, "darwin"), strings.Contains(low, "mac os"):
		if strings.Contains(low, "arm64") || strings.Contains(low, "aarch64") {
			return "darwin-arm64"
		}
		return "darwin-amd64"
	default:
		if strings.Contains(low, "aarch64") || strings.Contains(low, "arm64") {
			return "linux-arm64"
		}
		return "linux-amd64"
	}
}

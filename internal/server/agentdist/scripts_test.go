package agentdist

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
)

func TestEmbeddedInstallerParity(t *testing.T) {
	tests := []struct {
		name          string
		canonicalPath string
		embedded      string
		digestPattern string
		required      []string
	}{
		{
			name:          "linux",
			canonicalPath: filepath.Join("..", "..", "..", "scripts", "install-agent-linux.sh"),
			embedded:      installSh,
			digestPattern: `(?m)^CANONICAL_INSTALLER_SHA256='([0-9a-f]{64})'$`,
			required:      []string{"tunnel_name", "tunnel_node", "tunnel-enroll", "repo-credentials.env", "env_file", "install_bzip2", "refresh_agent_binaries", "agent_binary_version", "refreshed sm-agent binaries"},
		},
		{
			name:          "windows",
			canonicalPath: filepath.Join("..", "..", "..", "scripts", "install-agent-windows.ps1"),
			embedded:      installPs1,
			digestPattern: `(?m)^\$CanonicalInstallerSha256 = '([0-9a-f]{64})'$`,
			required:      []string{"tunnel_name", "tunnel_node", "tunnel-enroll", "repo-credentials.env", "env_file", "refreshBinaries", "refreshed sm-agent binaries"},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			canonical, err := os.ReadFile(test.canonicalPath)
			if err != nil {
				t.Fatalf("read canonical installer: %v", err)
			}
			match := regexp.MustCompile(test.digestPattern).FindStringSubmatch(test.embedded)
			if len(match) != 2 {
				t.Fatal("embedded installer is missing its canonical installer digest")
			}
			normalized := strings.ReplaceAll(string(canonical), "\r\n", "\n")
			got := fmt.Sprintf("%x", sha256.Sum256([]byte(normalized)))
			if got != match[1] {
				t.Fatalf("embedded installer is stale: canonical digest is %s, embedded installer acknowledges %s; re-sync the served installer with %s and update its canonical installer digest", got, match[1], test.canonicalPath)
			}
			for _, required := range test.required {
				if !strings.Contains(test.embedded, required) {
					t.Errorf("embedded installer is missing required parity behavior %q", required)
				}
			}
		})
	}
}

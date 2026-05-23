package agentdist

import (
	_ "embed"
	"strings"
)

//go:embed scripts/install.sh
var installSh string

//go:embed scripts/install.ps1
var installPs1 string

func RenderShellInstaller(serverURL string) string {
	return strings.ReplaceAll(installSh, "__SERVER_URL__", serverURL)
}

func RenderPowerShellInstaller(serverURL string) string {
	return strings.ReplaceAll(installPs1, "__SERVER_URL__", serverURL)
}

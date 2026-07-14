package backup

import (
	"fmt"
	"log/slog"
	"os"
	"strings"
)

func HostRootFromEnv(getenv func(string) string) string {
	return strings.TrimRight(strings.TrimSpace(getenv("SM_HOST_FS_ROOT")), "/")
}

func DetectContainer(getenv func(string) string, stat func(string) error) bool {
	if HostRootFromEnv(getenv) != "" {
		return true
	}
	for _, marker := range []string{"/.dockerenv", "/run/.containerenv"} {
		if stat(marker) == nil {
			return true
		}
	}
	return false
}

func validateHostRoot(hostRoot string) error {
	if hostRoot == "" {
		return nil
	}
	if hostRoot == "/" || !strings.HasPrefix(hostRoot, "/") {
		return fmt.Errorf("SM_HOST_FS_ROOT=%q is invalid: it must be an absolute path to the host mount such as /host", hostRoot)
	}
	for _, seg := range strings.Split(hostRoot, "/") {
		if seg == ".." {
			return fmt.Errorf("SM_HOST_FS_ROOT=%q is invalid: it must not contain '..' path segments", hostRoot)
		}
	}
	return nil
}

func IsContainerized() bool {
	return DetectContainer(os.Getenv, func(path string) error { _, err := os.Stat(path); return err })
}

func BaseOptions(logger *slog.Logger) Options {
	return Options{
		Logger:        logger,
		HostRoot:      HostRootFromEnv(os.Getenv),
		Containerized: IsContainerized(),
	}
}

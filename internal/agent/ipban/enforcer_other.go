//go:build !linux

package ipban

func NewEnforcer() (Enforcer, error) {
	return nil, ErrUnsupported
}

func Teardown() error {
	return ErrUnsupported
}

func Snapshot() ([]SetSnapshot, error) {
	return nil, ErrUnsupported
}

func isUnsupportedErrno(error) bool {
	return false
}

func HasNetAdmin() (bool, bool) {
	return false, false
}

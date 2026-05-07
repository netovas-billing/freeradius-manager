package manager

import "regexp"

var nameRE = regexp.MustCompile(`^[a-z0-9_]{1,32}$`)

var reservedNames = map[string]struct{}{
	"default":      {},
	"inner-tunnel": {},
	"control":      {},
	"status":       {},
	"radius":       {},
	"mysql":        {},
}

func validateName(name string) error {
	if !nameRE.MatchString(name) {
		return ErrInvalidName
	}
	if _, ok := reservedNames[name]; ok {
		return ErrInvalidName
	}
	return nil
}

// CreateInstance: implemented in create.go.
// DeleteInstance: implemented in delete.go.

// Lifecycle ops (Start/Stop/Restart/Test) implemented in lifecycle.go.

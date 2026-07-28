package app

import (
	"errors"
	"path/filepath"
	"strings"
)

const runtimeConfigDir = "/etc/amnezia/amneziawg"

func validTunnelInterfaceName(name string) bool {
	return tunnelNameRE.MatchString(name) && !strings.Contains(name, "..")
}

func validateTunnelInterfaceName(name string) error {
	if validTunnelInterfaceName(name) {
		return nil
	}
	return errors.New("tunnel name must start with a letter and contain only letters, numbers, dots, underscores, or dashes; consecutive dots are not allowed")
}

func runtimeConfigPath(interfaceName string) (string, error) {
	if err := validateTunnelInterfaceName(interfaceName); err != nil {
		return "", err
	}
	return filepath.Join(runtimeConfigDir, interfaceName+".conf"), nil
}

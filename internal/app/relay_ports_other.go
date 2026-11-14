//go:build !linux

package app

func lockRelayNetwork() (func(), error) {
	return func() {}, nil
}

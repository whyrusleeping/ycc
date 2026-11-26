//go:build !linux && !darwin && !freebsd

package jobshim

import "errors"

// peerUID fails closed where peer credentials cannot be read.
func peerUID(int) (int, error) {
	return -1, errors.New("unix socket peer credentials are unsupported on this platform")
}

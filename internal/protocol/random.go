package protocol

import (
	"crypto/rand"
	"encoding/hex"
	"errors"
	"math/big"
)

func randomInt(min, max int) (int, error) {
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max-min+1)))
	if err != nil {
		return 0, err
	}
	return min + int(n.Int64()), nil
}

func randomUint32Below(max uint32) (uint32, error) {
	if max == 0 {
		return 0, errors.New("random upper bound must be greater than zero")
	}
	n, err := rand.Int(rand.Reader, big.NewInt(int64(max)))
	if err != nil {
		return 0, err
	}
	return uint32(n.Int64()), nil
}

func randomUint32Range(min, max uint32) (uint32, error) {
	if max < min {
		return 0, errors.New("random range max must be greater than or equal to min")
	}
	width := uint64(max) - uint64(min) + 1
	n, err := rand.Int(rand.Reader, new(big.Int).SetUint64(width))
	if err != nil {
		return 0, err
	}
	return min + uint32(n.Uint64()), nil
}

func randomHexBytes(size int) (string, error) {
	b := make([]byte, size)
	if _, err := rand.Read(b); err != nil {
		return "", err
	}
	return hex.EncodeToString(b), nil
}

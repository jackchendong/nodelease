package nodelease

import (
	"crypto/rand"
	"encoding/base64"
	"fmt"
	"math/big"
)

const tokenBytes = 32

func randomToken() (string, error) {
	buffer := make([]byte, tokenBytes)
	if _, err := rand.Read(buffer); err != nil {
		return "", fmt.Errorf("generate lease token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buffer), nil
}

func randomOffset(size int64) (int64, error) {
	if size <= 1 {
		return 0, nil
	}
	value, err := rand.Int(rand.Reader, big.NewInt(size))
	if err != nil {
		return 0, fmt.Errorf("choose random worker ID: %w", err)
	}
	return value.Int64(), nil
}

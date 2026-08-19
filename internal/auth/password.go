package auth

import (
	"errors"

	"golang.org/x/crypto/bcrypt"
)

func HashPassword(password string) (string, error) {
	if password == "" {
		return "", ErrCredentials
	}
	digest, err := bcrypt.GenerateFromPassword([]byte(password), bcrypt.DefaultCost)
	if err != nil {
		return "", err
	}
	return string(digest), nil
}
func CheckPassword(digest, password string) bool {
	return bcrypt.CompareHashAndPassword([]byte(digest), []byte(password)) == nil
}

var ErrCredentials = errors.New("invalid credentials")

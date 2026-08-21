package domain

import "errors"

var (
	ErrNotFound     = errors.New("record not found")
	ErrConflict     = errors.New("state conflict")
	ErrUnauthorized = errors.New("unauthorized")
	ErrForbidden    = errors.New("forbidden")
	ErrInvalid      = errors.New("invalid request")
	ErrExpired      = errors.New("session expired")
	ErrCanceled     = errors.New("operation canceled")
	ErrInternal     = errors.New("internal failure")
)

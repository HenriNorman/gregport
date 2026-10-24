package auth

import "errors"

var (
	ErrEmailTaken     = errors.New("email already registered")
	ErrSlugTaken      = errors.New("organization slug already taken")
	ErrInvalidInput   = errors.New("invalid input")
	ErrWeakPassword   = errors.New("password too weak")
	ErrInvalidEmail   = errors.New("invalid email format")
	ErrEmptyName      = errors.New("name cannot be empty")
)

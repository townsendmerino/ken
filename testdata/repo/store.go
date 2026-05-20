package store

import "errors"

// ErrMissing is returned when a key is absent.
var ErrMissing = errors.New("missing key")

type Store struct {
	data map[string]string
}

// NewStore builds an empty Store.
func NewStore() *Store {
	return &Store{data: map[string]string{}}
}

func (s *Store) Get(key string) (string, error) {
	v, ok := s.data[key]
	if !ok {
		return "", ErrMissing
	}
	return v, nil
}

func (s *Store) Put(key, value string) {
	s.data[key] = value
}

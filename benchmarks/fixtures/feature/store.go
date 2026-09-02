package feature

import "sync"

type Store struct {
	mu    sync.RWMutex
	items map[string]string
}

func New() *Store { return &Store{items: make(map[string]string)} }

func (store *Store) Put(key, value string) {
	store.mu.Lock()
	defer store.mu.Unlock()
	store.items[key] = value
}

func (store *Store) Get(key string) (string, bool) {
	store.mu.RLock()
	defer store.mu.RUnlock()
	value, found := store.items[key]
	return value, found
}

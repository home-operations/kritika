package egress

import "sync"

// keyLocks hands out one lock per key for as long as anyone holds or waits
// for it, so the work on one entry, its fills or its uploads, goes one at a
// time while other entries' work does not wait.
type keyLocks struct {
	mu   sync.Mutex
	held map[string]*keyLock
}

type keyLock struct {
	sync.Mutex
	// users counts the holders and waiters; the lock goes once none remain.
	users int
}

// lock takes key's lock, waiting for its holder.
func (l *keyLocks) lock(key string) *keyLock {
	l.mu.Lock()
	if l.held == nil {
		l.held = map[string]*keyLock{}
	}
	k := l.held[key]
	if k == nil {
		k = &keyLock{}
		l.held[key] = k
	}
	k.users++
	l.mu.Unlock()
	k.Lock()
	return k
}

// unlock gives back key's lock, taken by lock.
func (l *keyLocks) unlock(key string, k *keyLock) {
	k.Unlock()
	l.mu.Lock()
	defer l.mu.Unlock()
	k.users--
	if k.users == 0 {
		delete(l.held, key)
	}
}

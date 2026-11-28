package storage

import (
	"context"
	"sync"
)

type headChangeScope struct{ owner, library string }

type headChangeWaiters struct {
	mu        sync.Mutex
	byLibrary map[headChangeScope]*headChangeGeneration
}

type headChangeGeneration struct {
	changed chan struct{}
	refs    int
}

// WaitForHeadChange reads the current Head and, while it matches after, waits for
// publication or context cancellation. An empty string denotes an empty Head.
func (s *Store) WaitForHeadChange(ctx context.Context, owner, library, after string) (string, error) {
	scope := headChangeScope{owner, library}
	for ctx.Err() == nil {
		// Subscribe before reading so a publication between the read and the wait
		// closes changed. SQLite remains the source of truth after every wakeup.
		changed, release := s.headChanges.subscribe(scope)
		current, err := s.GetLibrary(ctx, owner, library)
		if err != nil {
			release()
			return "", err
		}
		id := ""
		if current.HeadCommitID != nil {
			id = *current.HeadCommitID
		}
		if id != after {
			release()
			return id, nil
		}
		select {
		case <-changed:
		case <-ctx.Done():
		}
		release()
	}
	return "", ctx.Err()
}

func (w *headChangeWaiters) subscribe(scope headChangeScope) (<-chan struct{}, func()) {
	w.mu.Lock()
	if w.byLibrary == nil {
		w.byLibrary = make(map[headChangeScope]*headChangeGeneration)
	}
	generation := w.byLibrary[scope]
	if generation == nil {
		generation = &headChangeGeneration{changed: make(chan struct{})}
		w.byLibrary[scope] = generation
	}
	generation.refs++
	w.mu.Unlock()
	return generation.changed, func() {
		w.mu.Lock()
		defer w.mu.Unlock()
		generation.refs--
		if generation.refs == 0 && w.byLibrary[scope] == generation {
			delete(w.byLibrary, scope)
		}
	}
}

func (w *headChangeWaiters) notify(scope headChangeScope) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if generation := w.byLibrary[scope]; generation != nil {
		delete(w.byLibrary, scope)
		close(generation.changed)
	}
}

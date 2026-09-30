// field_memory.go decides which memory a turn in a Field can see and write
// (issue #133), and builds the tool closures that enforce it. The memory
// tool itself is unchanged — it only ever sees the five closures on
// tools.Context — so everything field-specific lives here.
package gateway

import (
	"errors"

	"polaris/store"
)

// memoryAccess is what a turn's memory tool can actually do, after the
// field's memory_mode and the global Memory switch are both applied.
type memoryAccess int

const (
	memoryNone      memoryAccess = iota // no memory tool, no {memories} block
	memoryGlobal                        // the global store, read/write — the pre-fields behavior
	memoryFieldOnly                     // the field's own store, read/write
	memoryBoth                          // field's store read/write, global read-only
)

// resolveMemoryAccess applies the global Memory switch to a field's mode.
// The mode never overrides the operator's switch: with it off, "default"
// has nothing left to show and "both" falls back to the field's own store,
// while "field_only" is unaffected (it never touched global memory).
// A turn outside any field (field == nil) behaves as "default".
func resolveMemoryAccess(field *store.Field, globalOn bool) memoryAccess {
	mode := store.FieldMemoryDefault
	if field != nil {
		mode = field.MemoryMode
	}
	switch mode {
	case store.FieldMemoryNone:
		return memoryNone
	case store.FieldMemoryFieldOnly:
		return memoryFieldOnly
	case store.FieldMemoryBoth:
		if globalOn {
			return memoryBoth
		}
		return memoryFieldOnly
	default:
		if globalOn {
			return memoryGlobal
		}
		return memoryNone
	}
}

// memoryClosures are the five callbacks tools.Context's memory tool uses.
type memoryClosures struct {
	list   func() ([]store.MemoryIndexEntry, error)
	get    func(name string) (*store.Memory, error)
	write  func(name, memType, description, content, occurredAt string) error
	edit   func(name, memType, description, content, occurredAt string) error
	forget func(name string) error
}

// newMemoryClosures binds the memory tool to the stores access allows.
// field may be nil only for memoryGlobal.
func newMemoryClosures(db *store.Store, field *store.Field, access memoryAccess) memoryClosures {
	switch access {
	case memoryGlobal:
		return memoryClosures{db.ListMemories, db.GetMemory, db.CreateMemory, db.UpdateMemory, db.DeleteMemory}
	case memoryFieldOnly:
		return fieldOnlyClosures(db, field.ID)
	case memoryBoth:
		return bothClosures(db, field.ID)
	}
	return memoryClosures{}
}

func fieldOnlyClosures(db *store.Store, fieldID string) memoryClosures {
	return memoryClosures{
		list: func() ([]store.MemoryIndexEntry, error) { return db.ListFieldMemories(fieldID) },
		get:  func(name string) (*store.Memory, error) { return db.GetFieldMemory(fieldID, name) },
		write: func(name, memType, description, content, occurredAt string) error {
			return db.CreateFieldMemory(fieldID, name, memType, description, content, occurredAt)
		},
		edit: func(name, memType, description, content, occurredAt string) error {
			return db.UpdateFieldMemory(fieldID, name, memType, description, content, occurredAt)
		},
		forget: func(name string) error { return db.DeleteFieldMemory(fieldID, name) },
	}
}

// bothClosures merges the two stores for reads (field first) but only ever
// writes the field's. A field thread can't touch a global memory: the
// global list is an operator-wide record, and a field thread quietly
// editing it would leak one field's context into every other conversation.
func bothClosures(db *store.Store, fieldID string) memoryClosures {
	fieldOnly := fieldOnlyClosures(db, fieldID)
	return memoryClosures{
		list: func() ([]store.MemoryIndexEntry, error) {
			fieldEntries, err := db.ListFieldMemories(fieldID)
			if err != nil {
				return nil, err
			}
			globalEntries, err := db.ListMemories()
			if err != nil {
				return nil, err
			}
			// Both stores are tagged here (and only here) because this is
			// the one mode where the model has to tell them apart — it's
			// also what makes a same-named pair distinguishable in the index.
			merged := make([]store.MemoryIndexEntry, 0, len(fieldEntries)+len(globalEntries))
			for _, e := range fieldEntries {
				e.Scope = store.MemoryScopeField
				merged = append(merged, e)
			}
			for _, e := range globalEntries {
				e.Scope = store.MemoryScopeGlobal
				merged = append(merged, e)
			}
			return merged, nil
		},
		// The field's entry wins a bare view of a name in both stores; the
		// global one stays reachable only by not having a field twin. Write
		// below rejects new collisions, so this only matters for a global
		// memory saved after a same-named field one.
		get: func(name string) (*store.Memory, error) {
			m, err := fieldOnly.get(name)
			if errors.Is(err, store.ErrMemoryNotFound) {
				return db.GetMemory(name)
			}
			return m, err
		},
		write: func(name, memType, description, content, occurredAt string) error {
			if _, err := db.GetMemory(name); err == nil {
				return store.ErrMemoryNameInGlobal
			}
			return fieldOnly.write(name, memType, description, content, occurredAt)
		},
		edit: func(name, memType, description, content, occurredAt string) error {
			return withGlobalReadOnlyHint(db, name, fieldOnly.edit(name, memType, description, content, occurredAt))
		},
		forget: func(name string) error {
			return withGlobalReadOnlyHint(db, name, fieldOnly.forget(name))
		},
	}
}

// withGlobalReadOnlyHint turns "not found in the field's store" into the
// more useful "that's a global memory, read-only here" when the name does
// exist globally — without it the model would be told the memory doesn't
// exist while its own index shows it, and likely retry or rewrite it.
func withGlobalReadOnlyHint(db *store.Store, name string, err error) error {
	if errors.Is(err, store.ErrMemoryNotFound) {
		if _, gerr := db.GetMemory(name); gerr == nil {
			return store.ErrGlobalMemoryReadOnly
		}
	}
	return err
}

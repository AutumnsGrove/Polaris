package gateway

import (
	"sync"
	"testing"

	"polaris/store"
)

// TestWeaverToolClosures_SeenStars_ConcurrentAccess reproduces the data race
// on seenStars in weaverToolClosures: agent.Run's dispatchToolCallsConcurrently
// fans out every tool call in one model turn across goroutines (parallel_tool_calls
// is requested as true — see llm/client.go), so two Weaver tool calls batched
// in the same turn (e.g. two search_stars, or search_stars + read_star) hit
// this shared map concurrently with no synchronization. Run with -race.
func TestWeaverToolClosures_SeenStars_ConcurrentAccess(t *testing.T) {
	db := openTestStoreForConstellation(t)
	// Seed real stars so search/read's markSeen calls actually execute —
	// against an empty DB neither closure ever writes to seenStars, so the
	// race never fires no matter how many goroutines pile on.
	var ids []int64
	for i := 0; i < 5; i++ {
		id, err := db.CreateStar(store.Star{
			Title: "star", Category: "identity", Summary: "s", Body: "b",
			Confidence: "medium", Status: "auto",
		})
		if err != nil {
			t.Fatalf("CreateStar: %v", err)
		}
		ids = append(ids, id)
	}

	search, read, _, _, _ := weaverToolClosures(db, "thread-1")

	var wg sync.WaitGroup
	for i := 0; i < 20; i++ {
		i := i
		wg.Add(2)
		go func() {
			defer wg.Done()
			_, _ = search("star")
		}()
		go func() {
			defer wg.Done()
			_, _ = read(ids[i%len(ids)])
		}()
	}
	wg.Wait()
}

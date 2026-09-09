package tools

import (
	"strings"
	"sync"
	"testing"
)

func TestArtifactStoreEvictionIsExplicit(t *testing.T) {
	store := NewArtifactStore()
	data := []byte(strings.Repeat("x", maxCapturedCommandBytes))
	first := store.put(data, int64(len(data)), 1, digestHex(data), "")
	for i := 0; i < maxArtifactStoreBytes/maxCapturedCommandBytes; i++ {
		store.put(data, int64(len(data)), 1, digestHex(data), "")
	}
	got, ok := store.get(first.id)
	if !ok || !got.evicted || !strings.Contains(got.lost, "retention limit") || len(got.data) != 0 {
		t.Fatalf("evicted artifact was not retained as an explicit tombstone: ok=%v artifact=%+v", ok, got)
	}
}

func TestArtifactStoreBoundsAllRetentionBookkeeping(t *testing.T) {
	store := NewArtifactStore()
	data := []byte("x")
	for i := 0; i < maxArtifactRecords*10; i++ {
		store.put(data, 1, 1, digestHex(data), "")
	}
	store.mu.Lock()
	defer store.mu.Unlock()
	if len(store.items) > maxArtifactRecords || len(store.recordOrder) > maxArtifactRecords || len(store.order) > maxArtifactRecords {
		t.Fatalf("retention bookkeeping grew past record cap: items=%d records=%d retained-order=%d", len(store.items), len(store.recordOrder), len(store.order))
	}
}

func TestArtifactCompletionAndEvictionReturnImmutableSnapshots(t *testing.T) {
	store := NewArtifactStore()
	data := []byte(strings.Repeat("x", maxCapturedCommandBytes))
	id := store.reserve()
	started := make(chan struct{})
	var snapshot outputArtifact
	var wg sync.WaitGroup
	wg.Add(2)
	go func() {
		defer wg.Done()
		close(started)
		snapshot = store.complete(id, data, int64(len(data)), 1, digestHex(data), "")
	}()
	go func() {
		defer wg.Done()
		<-started
		for i := 0; i < maxArtifactStoreBytes/maxCapturedCommandBytes+2; i++ {
			store.put(data, int64(len(data)), 1, digestHex(data), "")
		}
	}()
	wg.Wait()
	if snapshot.id != id || snapshot.bytes != int64(len(data)) || snapshot.digest == "" {
		t.Fatalf("completion snapshot changed during concurrent eviction: %+v", snapshot)
	}
}

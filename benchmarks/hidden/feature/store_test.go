package feature

import "testing"

func TestDeleteReturnsPriorValueAndRemovesKey(t *testing.T) {
	store := New()
	store.Put("key", "value")
	value, found := store.Delete("key")
	if !found || value != "value" {
		t.Fatalf("Delete = %q, %t", value, found)
	}
	if _, found := store.Get("key"); found {
		t.Fatal("deleted key remains present")
	}
}

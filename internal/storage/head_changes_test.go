package storage

import (
	"testing"
	"time"
)

func TestPublishHeadNotifiesOnlyAfterSuccessfulCommit(t *testing.T) {
	store := openAccountStore(t)
	defer closeAccountStore(t, store)
	ctx, now := t.Context(), time.Now()
	owner := User{ID: "owner", Username: "alice", PasswordHash: "hash"}
	if err := store.CreateUser(ctx, owner, now); err != nil {
		t.Fatal(err)
	}
	const library = "library"
	if _, _, err := store.CreateLibrary(ctx, Library{ID: library, OwnerUserID: owner.ID, Name: library}, now); err != nil {
		t.Fatal(err)
	}

	scope := headChangeScope{owner.ID, library}
	changed, release := store.headChanges.subscribe(scope)
	otherOwner, releaseOtherOwner := store.headChanges.subscribe(headChangeScope{"other", library})
	defer releaseOtherOwner()
	otherLibrary, releaseOtherLibrary := store.headChanges.subscribe(headChangeScope{owner.ID, "other"})
	defer releaseOtherLibrary()

	// The missing role aborts the transaction after it has updated the Head row.
	if _, err := store.PublishHead(ctx, owner.ID, library, nil, 0, "failed", nil, now, HeadChange{}); err == nil {
		t.Fatal("invalid publication succeeded")
	}
	select {
	case <-changed:
		t.Fatal("rolled-back publication notified waiters")
	default:
	}

	// The signal must survive publication before the subscriber starts to wait.
	current, err := store.UpdateLibraryHead(ctx, owner.ID, library, nil, 0, "first", nil, now)
	if err != nil {
		t.Fatal(err)
	}
	select {
	case <-changed:
	default:
		t.Fatal("successful publication did not notify the subscribed waiter")
	}
	select {
	case <-otherOwner:
		t.Fatal("publication notified another owner")
	case <-otherLibrary:
		t.Fatal("publication notified another library")
	default:
	}

	next, releaseNext := store.headChanges.subscribe(scope)
	defer releaseNext()
	release() // Cleanup of the old generation must not unregister the new one.
	if _, err := store.UpdateLibraryHead(ctx, owner.ID, library, current.HeadCommitID, current.HeadVersion, "second", nil, now); err != nil {
		t.Fatal(err)
	}
	select {
	case <-next:
	default:
		t.Fatal("next subscriber missed a publication after old subscriber cleanup")
	}
}

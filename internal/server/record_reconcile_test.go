package server

import (
	"testing"
	"time"
)

// Records used to sync only when the public IP changed, so anything else about
// a record — a lowered TTL, a hand edit in the console, a value left behind by
// a failed sync — stayed wrong until the address moved. For a TTL that is the
// one event it existed to shorten, so it could never arrive in time.
func TestShouldSyncRecords(t *testing.T) {
	t.Run("first cycle reconciles", func(t *testing.T) {
		s := &Server{}
		if !s.shouldSyncRecords(false) {
			t.Error("a freshly started server should reconcile on its first cycle")
		}
	})

	t.Run("quiet cycle inside the interval does not", func(t *testing.T) {
		s := &Server{}
		s.shouldSyncRecords(false) // stamps the clock
		if s.shouldSyncRecords(false) {
			t.Error("reconciled twice in a row; each record costs an AWS call")
		}
	})

	t.Run("an IP change always syncs", func(t *testing.T) {
		s := &Server{}
		s.shouldSyncRecords(false)
		if !s.shouldSyncRecords(true) {
			t.Error("an address change must not wait for the reconcile interval")
		}
	})

	t.Run("due again once the interval has passed", func(t *testing.T) {
		s := &Server{}
		s.shouldSyncRecords(false)
		s.lastRecordReconcile = time.Now().Add(-recordReconcileInterval - time.Second)
		if !s.shouldSyncRecords(false) {
			t.Error("should reconcile once the interval has elapsed")
		}
	})
}

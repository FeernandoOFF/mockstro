package workspacecoord

import (
	"errors"
	"testing"
	"time"
)

func TestShutdownWaitsForReservationAndPermanentlyClosesAdmission(t *testing.T) {
	var c Coordinator
	release, err := c.Reserve()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := c.Reserve(); !errors.Is(err, ErrBusy) {
		t.Fatalf("overlapping reservation: %v", err)
	}
	started := make(chan struct{})
	done := make(chan struct{})
	go func() { close(started); c.Shutdown(func() {}); close(done) }()
	<-started
	select {
	case <-done:
		t.Fatal("shutdown finished while an operation held the reservation")
	default:
	}
	release()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("shutdown did not finish")
	}
	if _, err := c.Reserve(); !errors.Is(err, ErrClosed) {
		t.Fatalf("reservation after shutdown: %v", err)
	}
	c.Shutdown(func() { t.Fatal("cleanup ran twice") })
}

package main

import (
	"context"
	"testing"
	"time"
)

func TestCheckConcurrencyControllerHotIncreaseAndDecrease(t *testing.T) {
	controller := newCheckConcurrencyController(1)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	if !controller.Acquire(ctx) {
		t.Fatal("first acquire failed")
	}

	second := make(chan bool, 1)
	go func() { second <- controller.Acquire(ctx) }()
	select {
	case <-second:
		t.Fatal("second acquire should wait at limit 1")
	case <-time.After(30 * time.Millisecond):
	}

	controller.SetLimit(2)
	select {
	case ok := <-second:
		if !ok {
			t.Fatal("second acquire was cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("hot increase did not release waiting worker")
	}

	controller.SetLimit(1)
	third := make(chan bool, 1)
	go func() { third <- controller.Acquire(ctx) }()
	controller.Release()
	select {
	case <-third:
		t.Fatal("lowered limit should wait until active work falls below 1")
	case <-time.After(30 * time.Millisecond):
	}
	controller.Release()
	select {
	case ok := <-third:
		if !ok {
			t.Fatal("third acquire was cancelled")
		}
	case <-time.After(time.Second):
		t.Fatal("waiting worker did not resume after active work drained")
	}
	controller.Release()

	status := controller.Status()
	if anyToInt(status["limit"]) != 1 || anyToInt(status["active"]) != 0 || anyToInt(status["maximum"]) != 300 {
		t.Fatalf("unexpected status: %#v", status)
	}
}

func TestCheckConcurrencyControllerClampsRange(t *testing.T) {
	controller := newCheckConcurrencyController(0)
	if got := anyToInt(controller.Status()["limit"]); got != 1 {
		t.Fatalf("limit=%d want 1", got)
	}
	if got := controller.SetLimit(999); got != maxCheckConcurrency {
		t.Fatalf("limit=%d want %d", got, maxCheckConcurrency)
	}
}

package sdkapp

import (
	"context"
	"sync"
	"testing"
)

// The camera handoff bug is not a data race. Every memory access involved is
// already correctly synchronised; what is wrong is the order of two RPCs, so no
// race detector will ever flag it. Reproducing it needs a stand-in for the RPC
// and an assertion about ordering, which is why enableImageStreaming is a package
// variable rather than a func.

// camCalls records the enable/disable calls a test's fake receives.
type camCalls struct {
	mu   sync.Mutex
	seen []bool
}

func (c *camCalls) add(enable bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.seen = append(c.seen, enable)
}

func (c *camCalls) last() (bool, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.seen) == 0 {
		return false, false
	}
	return c.seen[len(c.seen)-1], true
}

// resetCamState clears the package globals this test file touches. Tests here
// mutate package state, so none of them may call t.Parallel.
func resetCamState(esn string) {
	robotsMu.Lock()
	defer robotsMu.Unlock()
	robots = []Robot{{ESN: esn}}
	camStreams = map[string]*camStream{}
	camOps = map[string]*sync.Mutex{}
	camGen = 0
}

// TestCamStreamHandoffKeepsCameraOn is the regression test for "serialize camera
// disable with the ownership handoff".
//
// A departing handler releases ownership and only then issues its disable. A
// replacement that claims in that window turns the camera on, and the departing
// handler's disable then lands on top and leaves the new owner with a dead feed.
// The invariant that catches it: whenever a live owner exists, the last thing said
// to the robot must have been "on".
//
// Without the op lock in startCamStream/finishCamStream this fails within a few
// iterations. The sleep in the fake disable widens the window so the failure is
// near deterministic rather than a rare flake.
func TestCamStreamHandoffKeepsCameraOn(t *testing.T) {
	const esn = "00e20100"
	robotObj := Robot{ESN: esn}

	calls := &camCalls{}
	orig := enableImageStreaming
	enableImageStreaming = func(_ Robot, enable bool) {
		if !enable {
			// Stand in for an RPC that takes a moment, which is exactly when the
			// replacement slips past.
			busyWait()
		}
		calls.add(enable)
	}
	t.Cleanup(func() { enableImageStreaming = orig })

	for i := 0; i < 60; i++ {
		resetCamState(esn)
		calls.mu.Lock()
		calls.seen = nil
		calls.mu.Unlock()

		// Establish the outgoing owner.
		_, oldCancel := context.WithCancel(context.Background())
		defer oldCancel()
		oldGen, _ := claimCamStream(esn, oldCancel)

		_, newCancel := context.WithCancel(context.Background())
		defer newCancel()

		var wg sync.WaitGroup
		wg.Add(2)
		// The departing handler unwinding.
		go func() {
			defer wg.Done()
			finishCamStream(robotObj, oldGen)
		}()
		// The replacement arriving.
		go func() {
			defer wg.Done()
			startCamStream(robotObj, newCancel)
		}()
		wg.Wait()

		robotsMu.Lock()
		owner := camStreams[esn]
		robotsMu.Unlock()
		if owner == nil {
			// The replacement lost the race outright and nobody owns the feed.
			// Leaving the camera off is correct in that case.
			continue
		}
		last, ok := calls.last()
		if !ok {
			t.Fatalf("iteration %d: an owner exists but the camera was never touched", i)
		}
		if !last {
			t.Fatalf("iteration %d: owner gen %d holds the feed but the last call to the robot was disable; "+
				"a departing handler switched the camera off underneath it", i, owner.gen)
		}
	}
}

// TestReleaseCamStreamIgnoresSupersededOwner pins the generation check that stops
// a handler which has already been displaced from clearing the new owner's state.
func TestReleaseCamStreamIgnoresSupersededOwner(t *testing.T) {
	const esn = "00e20100"
	resetCamState(esn)

	_, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	gen1, replaced := claimCamStream(esn, cancel1)
	if replaced {
		t.Fatal("first claim reported displacing an owner that did not exist")
	}

	_, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	gen2, replaced := claimCamStream(esn, cancel2)
	if !replaced {
		t.Fatal("second claim did not report displacing the first")
	}
	if gen2 == gen1 {
		t.Fatal("second claim reused the first generation")
	}

	if releaseCamStream(esn, gen1) {
		t.Fatal("the displaced handler was allowed to release the new owner's feed")
	}
	if !isCamStreaming(esn) {
		t.Fatal("the displaced handler cleared CamStreaming under the new owner")
	}
	if !releaseCamStream(esn, gen2) {
		t.Fatal("the current owner could not release its own feed")
	}
	if isCamStreaming(esn) {
		t.Fatal("CamStreaming still set after the owner released")
	}
}

// busyWait burns a short, predictable amount of time without sleeping, so the
// window stays open long enough to lose the race but the test stays fast.
func busyWait() {
	x := 0
	for i := 0; i < 400000; i++ {
		x += i
	}
	_ = x
}

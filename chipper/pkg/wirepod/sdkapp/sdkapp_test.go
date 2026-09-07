package sdkapp

import (
	"context"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
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

// resetSdkState clears the package globals this test file touches. Tests here
// mutate package state, so none of them may call t.Parallel.
func resetSdkState(esn string) {
	robotsMu.Lock()
	defer robotsMu.Unlock()
	robots = []Robot{{ESN: esn}}
	camStreams = map[string]*camStream{}
	camOps = map[string]*sync.Mutex{}
	camGen = 0
	eventStreams = map[string]*eventStream{}
	eventGen = 0
	camMeters = map[string]*camMeter{}
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
		resetSdkState(esn)
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
	resetSdkState(esn)

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

// --- stim event stream ---------------------------------------------------
//
// Unlike the camera handoff above, this one IS a genuine data race, so -race
// sees it. Be honest about what these cover though: they exercise the extracted
// runEventStream and the ownership rules around it. They cannot reproduce the
// original bug, because the loop used to be inline in the handler with nothing to
// inject a fake into; that one is shown by reading the old code, not by running
// anything.

// fakeReceiver stands in for ExternalInterface_EventStreamClient. Recv blocks
// until the test sends an event or the context is cancelled, which is exactly how
// the real client behaves on a robot that is not producing stim events.
type fakeReceiver struct {
	ctx    context.Context
	events chan float32
}

func (f *fakeReceiver) Recv() (*vectorpb.EventResponse, error) {
	select {
	case <-f.ctx.Done():
		return nil, f.ctx.Err()
	case v := <-f.events:
		return &vectorpb.EventResponse{
			Event: &vectorpb.Event{
				EventType: &vectorpb.Event_StimulationInfo{
					StimulationInfo: &vectorpb.StimulationInfo{
						Value: v,
						// Non-zero on purpose. The production loop decides a value is
						// present with strings.Contains(fmt.Sprint(stimInfo),
						// "velocity"), and proto3 omits zero-valued scalars from the
						// text form, so a zero here means the write is silently
						// skipped and the test would pass for the wrong reason.
						Velocity: 1,
					},
				},
			},
		}, nil
	}
}

// liveReceivers counts goroutines currently inside runEventStream.
type liveReceivers struct {
	mu   sync.Mutex
	n    int
	peak int
}

func (l *liveReceivers) enter() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n++
	if l.n > l.peak {
		l.peak = l.n
	}
	return l.n
}

func (l *liveReceivers) exit() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.n--
}

func (l *liveReceivers) peakSeen() int {
	l.mu.Lock()
	defer l.mu.Unlock()
	return l.peak
}

// TestEventStreamNeverHasTwoOwners is the regression test for "cancel the stopped
// event stream before admitting another".
//
// stop_event_stream used to only flip a boolean, which the receiving goroutine
// sampled at the top of its loop and therefore reached only after Recv returned.
// On a robot sending nothing that never happened, so the goroutine outlived its
// stop, the next begin admitted a second one, and when the first finally woke it
// saw the flag true again and carried on. Both then wrote StimState.
func TestEventStreamNeverHasTwoOwners(t *testing.T) {
	const esn = "00e20100"
	resetSdkState(esn)

	live := &liveReceivers{}

	for cycle := 0; cycle < 25; cycle++ {
		ctx, cancel := context.WithCancel(context.Background())
		gen, ok := claimEventStream(esn, cancel)
		if !ok {
			cancel()
			t.Fatalf("cycle %d: begin was refused even though the previous stop should have freed the stream", cycle)
		}
		recv := &fakeReceiver{ctx: ctx, events: make(chan float32)}

		done := make(chan struct{})
		go func() {
			defer close(done)
			live.enter()
			defer live.exit()
			runEventStream(esn, gen, recv)
		}()

		// Let the receiver get as far as blocking in Recv, which is the state the
		// old code could not get out of.
		waitFor(t, func() bool { return isEventStreaming(esn) })

		stopEventStream(esn)

		// The whole point of the fix: this receiver has to actually die. If it can
		// outlive its stop then repeated close/reopen cycles accumulate live RPCs,
		// which is the finding. Ownership is released synchronously by
		// stopEventStream so the next begin is never refused, which is why the wait
		// is here rather than an assertion that two goroutines never overlap: a
		// receiver that is unwinding owns nothing and its generation check stops it
		// writing anything.
		select {
		case <-done:
		case <-time.After(2 * time.Second):
			t.Fatalf("cycle %d: receiver still alive 2s after its stop", cycle)
		}
	}

	if p := live.peakSeen(); p > 1 {
		t.Fatalf("peak concurrent receivers was %d, want 1", p)
	}
	if isEventStreaming(esn) {
		t.Fatal("EventsStreaming still set after the final stop")
	}
}

// TestStopEventStreamEndsReceiverPromptly is the part the old code could not do at
// all: end a receiver that is parked in Recv on a robot sending nothing.
func TestStopEventStreamEndsReceiverPromptly(t *testing.T) {
	const esn = "00e20100"
	resetSdkState(esn)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	gen, ok := claimEventStream(esn, cancel)
	if !ok {
		t.Fatal("could not claim an unowned stream")
	}

	done := make(chan struct{})
	go func() {
		defer close(done)
		runEventStream(esn, gen, &fakeReceiver{ctx: ctx, events: make(chan float32)})
	}()

	waitFor(t, func() bool { return isEventStreaming(esn) })
	stopEventStream(esn)

	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("receiver did not exit after stopEventStream; it is still parked in Recv")
	}
	if isEventStreaming(esn) {
		t.Fatal("EventsStreaming still set after stop")
	}
}

// TestClaimEventStreamRefusesWhileOwned covers the double click: re-selecting Stim
// must reuse the running stream, not start a second one.
func TestClaimEventStreamRefusesWhileOwned(t *testing.T) {
	const esn = "00e20100"
	resetSdkState(esn)

	_, cancel1 := context.WithCancel(context.Background())
	defer cancel1()
	if _, ok := claimEventStream(esn, cancel1); !ok {
		t.Fatal("first claim refused")
	}
	_, cancel2 := context.WithCancel(context.Background())
	defer cancel2()
	if _, ok := claimEventStream(esn, cancel2); ok {
		t.Fatal("second claim was admitted while the stream was already owned")
	}

	stopEventStream(esn)

	_, cancel3 := context.WithCancel(context.Background())
	defer cancel3()
	if _, ok := claimEventStream(esn, cancel3); !ok {
		t.Fatal("claim refused after a stop; a begin right after a stop must not be turned away")
	}
}

// TestSupersededReceiverCannotWriteStimState pins the generation check on the
// write, so a receiver still unwinding cannot overwrite the current owner's value.
func TestSupersededReceiverCannotWriteStimState(t *testing.T) {
	const esn = "00e20100"
	resetSdkState(esn)

	_, cancelOld := context.WithCancel(context.Background())
	defer cancelOld()
	oldGen, _ := claimEventStream(esn, cancelOld)
	setStimStateIfOwner(esn, oldGen, 0.25)
	if got := stimState(esn); got != 0.25 {
		t.Fatalf("owner write did not land: got %v", got)
	}

	stopEventStream(esn)
	_, cancelNew := context.WithCancel(context.Background())
	defer cancelNew()
	newGen, ok := claimEventStream(esn, cancelNew)
	if !ok {
		t.Fatal("could not claim after stop")
	}
	setStimStateIfOwner(esn, newGen, 0.75)

	// The superseded receiver tries to publish one last reading.
	setStimStateIfOwner(esn, oldGen, 0.1)
	if got := stimState(esn); got != 0.75 {
		t.Fatalf("a superseded receiver overwrote the current owner's value: got %v, want 0.75", got)
	}
}

// waitFor polls a condition briefly. Used instead of a bare sleep so the tests do
// not depend on goroutine scheduling luck.
func waitFor(t *testing.T, cond func() bool) {
	t.Helper()
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		if cond() {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("condition not met within the deadline")
}

// --- camera byte meter -----------------------------------------------------
//
// These cover the state behind the throughput readout, not the endpoint that
// serves it. SdkapiHandler needs a real robot on the other end of a gRPC
// connection, so /api-sdk/net_probe itself is covered by inspection and by the
// live check against the robot, the same caveat as the camera and stim work.

// TestCamMeterKeepsRobotsApart pins the thing that made the old code key state by
// slice index a bug: two robots streaming at once must not be counted together.
func TestCamMeterKeepsRobotsApart(t *testing.T) {
	const esnA = "00e20100"
	const esnB = "00e20101"
	resetSdkState(esnA)

	a := getCamMeter(esnA)
	b := getCamMeter(esnB)
	if a == b {
		t.Fatal("two ESNs share one meter")
	}

	atomic.AddUint64(&a.bytes, 1500)
	atomic.AddUint64(&a.frames, 1)
	atomic.AddUint64(&b.bytes, 40)
	atomic.AddUint64(&b.frames, 2)

	if gotBytes, gotFrames := readCamMeter(esnA); gotBytes != 1500 || gotFrames != 1 {
		t.Fatalf("esnA: got %d bytes / %d frames, want 1500 / 1", gotBytes, gotFrames)
	}
	if gotBytes, gotFrames := readCamMeter(esnB); gotBytes != 40 || gotFrames != 2 {
		t.Fatalf("esnB: got %d bytes / %d frames, want 40 / 2", gotBytes, gotFrames)
	}

	// An ESN nobody has streamed reads zero rather than panicking, because
	// net_probe answers for a robot whose camera has never been opened.
	if gotBytes, gotFrames := readCamMeter("00e20102"); gotBytes != 0 || gotFrames != 0 {
		t.Fatalf("unseen esn: got %d bytes / %d frames, want 0 / 0", gotBytes, gotFrames)
	}
}

// TestCamMeterCountsExactlyUnderConcurrency is the one that earns -race. The
// frame loop adds without holding robotsMu, and net_probe reads from an HTTP
// handler on another goroutine, so the counters have to be genuinely atomic and
// not merely unlocked. Losing an update would show up here as a short total.
func TestCamMeterCountsExactlyUnderConcurrency(t *testing.T) {
	const esn = "00e20100"
	const writers = 8
	const perWriter = 2000
	const frameSize = 1234
	resetSdkState(esn)

	m := getCamMeter(esn)

	stop := make(chan struct{})
	var readerWg sync.WaitGroup
	readerWg.Add(1)
	go func() {
		defer readerWg.Done()
		for {
			select {
			case <-stop:
				return
			default:
				// Reads through the same accessor net_probe uses, so the map lookup
				// under robotsMu runs concurrently with the lock-free adds.
				readCamMeter(esn)
			}
		}
	}()

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for j := 0; j < perWriter; j++ {
				atomic.AddUint64(&m.bytes, frameSize)
				atomic.AddUint64(&m.frames, 1)
			}
		}()
	}
	wg.Wait()
	close(stop)
	readerWg.Wait()

	wantBytes := uint64(writers * perWriter * frameSize)
	wantFrames := uint64(writers * perWriter)
	gotBytes, gotFrames := readCamMeter(esn)
	if gotBytes != wantBytes || gotFrames != wantFrames {
		t.Fatalf("got %d bytes / %d frames, want %d / %d", gotBytes, gotFrames, wantBytes, wantFrames)
	}
}

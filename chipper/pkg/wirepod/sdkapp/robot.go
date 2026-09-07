package sdkapp

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/digital-dream-labs/hugh/grpc/client"
	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

var robots []Robot
var timerStopIndexes []int
var inhibitCreation bool

// robotsMu guards the shared state that outlives a single request: the
// CamStreaming flag, the camStreams registry below, and the two statements that
// replace the robots slice itself, so a reader can never see a half-written slice
// header. Handlers, the conn timer and the stream goroutines all reach this state
// from different goroutines.
//
// ConnTimer and BcAssumption are NOT guarded and are as unsynchronised as they
// were before; nothing here should be read as protecting them.
var robotsMu sync.Mutex

// One entry per robot that has a live /cam-stream handler, keyed by ESN rather
// than by a position in robots: removeRobot rebuilds that slice by filtering, so
// an index captured when the stream opened names a different robot after any
// earlier robot is dropped, and a camera-only page is dropped by connTimer after
// 300s because nothing on it resets ConnTimer.
type camStream struct {
	// The generation handed to the owning handler. Cleanup that finds a different
	// generation has been superseded and must leave the robot alone, or it turns
	// the camera off underneath the handler that replaced it.
	gen    uint64
	cancel context.CancelFunc
}

var camStreams = map[string]*camStream{}
var camGen uint64

// camOps holds one lock per robot, taken across "claim ownership and turn the
// camera on" and across "give ownership back and turn the camera off". Each of
// those is a registry update plus an RPC, and it is the RPC that has to be
// ordered: releaseCamStream can hand ownership to a replacement while the
// departing handler is still inside EnableImageStreaming(false), and that disable
// then lands on the new owner's feed and kills it.
//
// Entries are never removed. The key set is bounded by the robots that have ever
// opened a camera stream in this process, not by request count, so it does not
// grow without limit; and deleting a mutex another goroutine has already read out
// of this map would need a refcount under a further lock to be safe, which is more
// machinery than the few bytes it would reclaim. Do not add cleanup here.
var camOps = map[string]*sync.Mutex{}

// camOpMu must be called with robotsMu NOT held: it takes robotsMu itself, and
// every other path takes a camera op lock first and robotsMu second. Calling this
// from inside a region that already holds robotsMu would invert that order.
func camOpMu(esn string) *sync.Mutex {
	robotsMu.Lock()
	defer robotsMu.Unlock()
	mu, ok := camOps[esn]
	if !ok {
		mu = &sync.Mutex{}
		camOps[esn] = mu
	}
	return mu
}

// call with robotsMu held
func setCamStreamingLocked(esn string, streaming bool) {
	for i := range robots {
		if strings.EqualFold(esn, robots[i].ESN) {
			robots[i].CamStreaming = streaming
			return
		}
	}
}

func isCamStreaming(esn string) bool {
	robotsMu.Lock()
	defer robotsMu.Unlock()
	for i := range robots {
		if strings.EqualFold(esn, robots[i].ESN) {
			return robots[i].CamStreaming
		}
	}
	return false
}

// claimCamStream makes the caller the owner of this robot's camera feed. Whatever
// handler held it is cancelled rather than left hanging, which is the "explicitly
// stop the existing feed before starting another" half of the fix; the returned
// bool says whether there was one, because the robot needs a moment to drop the
// old CameraFeed before a new one is opened.
func claimCamStream(esn string, cancel context.CancelFunc) (uint64, bool) {
	robotsMu.Lock()
	prev := camStreams[esn]
	camGen++
	gen := camGen
	camStreams[esn] = &camStream{gen: gen, cancel: cancel}
	setCamStreamingLocked(esn, true)
	robotsMu.Unlock()
	if prev != nil {
		prev.cancel()
		return gen, true
	}
	return gen, false
}

// releaseCamStream drops ownership if the caller still holds it, and reports
// whether it did. Only the current owner may disable the robot's image streaming.
func releaseCamStream(esn string, gen uint64) bool {
	robotsMu.Lock()
	defer robotsMu.Unlock()
	cur := camStreams[esn]
	if cur == nil || cur.gen != gen {
		return false
	}
	delete(camStreams, esn)
	setCamStreamingLocked(esn, false)
	return true
}

// stopCamStream ends the feed for this robot if one is running. Clearing the flag
// alone is not enough: the handler only samples it after Recv returns, which never
// happens on a robot that is sending no frames, so cancel the stream context too.
func stopCamStream(esn string) {
	robotsMu.Lock()
	setCamStreamingLocked(esn, false)
	cur := camStreams[esn]
	robotsMu.Unlock()
	if cur != nil {
		cur.cancel()
	}
}

// One entry per robot with a live stim receiver. Keyed by ESN for the same reason
// camStreams is: the goroutine outlives the request that started it, and an index
// captured back then names a different robot once removeRobot filters the slice.
type eventStream struct {
	gen    uint64
	cancel context.CancelFunc
}

var eventStreams = map[string]*eventStream{}
var eventGen uint64

// call with robotsMu held
func setEventsStreamingLocked(esn string, streaming bool) {
	for i := range robots {
		if strings.EqualFold(esn, robots[i].ESN) {
			robots[i].EventsStreaming = streaming
			return
		}
	}
}

// claimEventStream gives the caller the robot's stim stream if nobody holds it.
// Unlike claimCamStream this deliberately does NOT displace the current owner: the
// camera has no stop protocol, so a reloaded <img> must be able to take the feed,
// whereas the stim graph has an explicit stop_event_stream and a second begin is
// just a double click, which should cost nothing rather than tear down a working
// stream and blank the graph.
//
// Folding the old "already running" check into the claim also closes the
// check-then-act window between reading the flag and setting it, where two
// simultaneous begins could both read false.
func claimEventStream(esn string, cancel context.CancelFunc) (uint64, bool) {
	robotsMu.Lock()
	defer robotsMu.Unlock()
	if eventStreams[esn] != nil {
		return 0, false
	}
	eventGen++
	gen := eventGen
	eventStreams[esn] = &eventStream{gen: gen, cancel: cancel}
	setEventsStreamingLocked(esn, true)
	return gen, true
}

// releaseEventStream drops ownership if the caller still holds it. Unlike the
// camera this needs no operation lock, and the reason is structural rather than
// incidental: the camera's off switch is a separate RPC that can be issued and
// then land late, while this stream's off switch is cancelling its context, which
// can never arrive too late to matter.
func releaseEventStream(esn string, gen uint64) bool {
	robotsMu.Lock()
	defer robotsMu.Unlock()
	cur := eventStreams[esn]
	if cur == nil || cur.gen != gen {
		return false
	}
	delete(eventStreams, esn)
	setEventsStreamingLocked(esn, false)
	return true
}

// stopEventStream cancels the receiver and frees ownership in the same critical
// section. Releasing here rather than leaving it to the goroutine is what stops a
// begin arriving straight after a stop from being refused because the old receiver
// has not woken up yet, which would leave the poller reading "must start event
// stream" until it gave up.
func stopEventStream(esn string) {
	robotsMu.Lock()
	cur := eventStreams[esn]
	delete(eventStreams, esn)
	setEventsStreamingLocked(esn, false)
	setStimStateLocked(esn, 0)
	robotsMu.Unlock()
	if cur != nil {
		cur.cancel()
	}
}

func isEventStreaming(esn string) bool {
	robotsMu.Lock()
	defer robotsMu.Unlock()
	for i := range robots {
		if strings.EqualFold(esn, robots[i].ESN) {
			return robots[i].EventsStreaming
		}
	}
	return false
}

func stimState(esn string) float32 {
	robotsMu.Lock()
	defer robotsMu.Unlock()
	for i := range robots {
		if strings.EqualFold(esn, robots[i].ESN) {
			return robots[i].StimState
		}
	}
	return 0
}

// call with robotsMu held
func setStimStateLocked(esn string, value float32) {
	for i := range robots {
		if strings.EqualFold(esn, robots[i].ESN) {
			robots[i].StimState = value
			return
		}
	}
}

// setStimStateIfOwner drops writes from a receiver that has been superseded, so a
// goroutine still unwinding from a cancelled Recv cannot overwrite the value the
// current owner has just published.
func setStimStateIfOwner(esn string, gen uint64, value float32) {
	robotsMu.Lock()
	defer robotsMu.Unlock()
	cur := eventStreams[esn]
	if cur == nil || cur.gen != gen {
		return
	}
	setStimStateLocked(esn, value)
}

type Robot struct {
	ESN               string
	GUID              string
	Target            string
	Vector            *vector.Vector
	BcAssumption      bool
	CamStreaming      bool
	EventStreamClient vectorpb.ExternalInterface_EventStreamClient
	EventsStreaming   bool
	StimState         float32
	ConnTimer         int32
	Ctx               context.Context
}

func newRobot(serial string) (Robot, int, error) {
	inhibitCreation = true
	var RobotObj Robot

	// generate context
	RobotObj.Ctx = context.Background()

	// find robot info in BotInfo
	matched := false
	for _, robot := range vars.BotInfo.Robots {
		if strings.EqualFold(serial, robot.Esn) {
			RobotObj.ESN = strings.TrimSpace(strings.ToLower(serial))
			RobotObj.Target = robot.IPAddress + ":443"
			matched = true
			if robot.GUID == "" {
				robot.GUID = vars.BotInfo.GlobalGUID
				RobotObj.GUID = vars.BotInfo.GlobalGUID
			} else {
				RobotObj.GUID = robot.GUID
			}
			logger.Info("sdkapp", serial, "connecting, GUID "+RobotObj.GUID)
		}
	}
	if !matched {
		inhibitCreation = false
		return RobotObj, 0, fmt.Errorf("error: robot not found in SDK info file")
	}

	// create Vector instance
	var err error
	RobotObj.Vector, err = vector.New(
		vector.WithTarget(RobotObj.Target),
		vector.WithSerialNo(RobotObj.ESN),
		vector.WithToken(RobotObj.GUID),
	)
	if err != nil {
		inhibitCreation = false
		return RobotObj, 0, err
	}

	// connection check
	_, err = RobotObj.Vector.Conn.BatteryState(context.Background(), &vectorpb.BatteryStateRequest{})
	if err != nil {
		inhibitCreation = false
		return RobotObj, 0, err
	}

	// create client for event stream
	RobotObj.EventStreamClient, err = RobotObj.Vector.Conn.EventStream(
		RobotObj.Ctx,
		&vectorpb.EventRequest{
			ListType: &vectorpb.EventRequest_WhiteList{
				WhiteList: &vectorpb.FilterList{
					// this will be used only for stimulation graph for now
					List: []string{"stimulation_info"},
				},
			},
		},
	)
	if err != nil {
		inhibitCreation = false
		return RobotObj, 0, err
	}
	RobotObj.CamStreaming = false
	RobotObj.EventsStreaming = false

	// we have confirmed robot connection works, append to list of bots
	// Under robotsMu so the readers of the slice header (isCamStreaming and friends)
	// cannot observe it mid-write when append reallocates.
	robotsMu.Lock()
	robots = append(robots, RobotObj)
	robotIndex := len(robots) - 1
	robotsMu.Unlock()

	// begin inactivity timer
	go connTimer(robotIndex)

	inhibitCreation = false
	return RobotObj, robotIndex, nil
}

func getRobot(serial string) (Robot, int, error) {
	// look in robot list
	for {
		if !inhibitCreation {
			break
		}
		time.Sleep(time.Second / 2)
	}
	for index, robot := range robots {
		if strings.EqualFold(serial, robot.ESN) {
			return robot, index, nil
		}
	}
	return newRobot(serial)
}

// if connection is inactive for more than 5 minutes, remove robot
// run this as a goroutine
func connTimer(ind int) {
	// Check if the index is in the list
	if len(robots) <= ind {
		return
	}

	robots[ind].ConnTimer = 0
	for {
		time.Sleep(time.Second)
		// check if timer needs to be stopped
		for _, num := range timerStopIndexes {
			if num == ind {
				logger.Debug("sdkapp", robots[ind].ESN, "conn timer stopping, index "+strconv.Itoa(ind))
				var newIndexes []int
				for _, num := range timerStopIndexes {
					if num != ind {
						newIndexes = append(newIndexes, num)
					}
				}
				timerStopIndexes = newIndexes
				return
			}
		}
		if robots[ind].ConnTimer >= 300 {
			logger.Debug("sdkapp", robots[ind].ESN, "closing SDK connection, source: connTimer")
			removeRobot(robots[ind].ESN, "connTimer")
			return
		}  
		robots[ind].ConnTimer = robots[ind].ConnTimer + 1
	}
}

func removeRobot(serial, source string) {
	inhibitCreation = true
	var newRobots []Robot
	for ind, robot := range robots {
		if !strings.EqualFold(serial, robot.ESN) {
			newRobots = append(newRobots, robot)
		} else {
			if source == "server" {
				timerStopIndexes = append(timerStopIndexes, ind)
			}
			// Cancels the feed as well as clearing the flag: a handler parked in
			// Recv on a robot that sends no frames cannot see the flag at all.
			stopCamStream(robots[ind].ESN)
			// Cancels the receiver as well as clearing the flag, matching stopCamStream
			// on the line above: a goroutine parked in Recv cannot see the flag at all.
			stopEventStream(robots[ind].ESN)
			robots[ind].BcAssumption = false
			// give time for all of that to stop
			time.Sleep(time.Second * 3)
		}
	}
	robotsMu.Lock()
	robots = newRobots
	robotsMu.Unlock()
	inhibitCreation = false
}

func NewWP(serial string, useGlobal bool) (*vector.Vector, error) {
	var target, guid string
	if serial == "" {
		return nil, fmt.Errorf("serial string missing")
	}
	matched := false
	for _, robot := range vars.BotInfo.Robots {
		if strings.EqualFold(serial, robot.Esn) {
			matched = true
			target = robot.IPAddress + ":443"
			guid = robot.GUID
			break
		}
	}
	if !matched {
		logger.Error("sdkapp", serial, "serial did not match any bot in bot json")
		return nil, errors.New("serial did not match any bot in bot json")
	}
	c, err := client.New(
		client.WithTarget(target),
		client.WithInsecureSkipVerify(),
	)
	if err != nil {
		return nil, err
	}
	if err := c.Connect(); err != nil {
		return nil, err
	}
	return vector.New(
		vector.WithTarget(target),
		vector.WithSerialNo(serial),
		vector.WithToken(guid),
	)
}

package sdkapp

import (
	"context"
	"fmt"
	"math"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
)

// polls each online bot's battery and drives it onto the charger when the
// percentage stays at/below vars.APIConfig.Battery.GoHomePercent (0 = disabled)

const (
	bwPollInterval   = 30 * time.Second
	bwRPCTimeout     = 5 * time.Second
	bwDockTimeout    = 3 * time.Minute
	bwHysteresisN    = 3
	bwCooldown       = 10 * time.Minute
	bwGiveUpCooldown = 30 * time.Minute
	bwMaxAttempts    = 3
)

type bwBotState struct {
	consecutiveLow int
	attempts       int
	coolingUntil   time.Time
	docking        bool
	// charger-transition tracking; haveReading avoids logging a
	// transition on the first poll after startup
	haveReading bool
	lastHome    bool
}

type bwCachedConn struct {
	robot *vector.Vector
	ip    string
}

var (
	bwMu     sync.Mutex
	bwStates = make(map[string]*bwBotState)
	// vector.New leaks its grpc conn (no Close exposed), so connections are
	// cached per ESN and redialed only on IP change or RPC error
	bwConns = make(map[string]*bwCachedConn)
)

// must match getBatteryPercentage in webroot/js/battery.js so the trigger
// percent agrees with what the web UI shows
func bwBatteryPercent(volts float32) int {
	const maxVoltage, midVoltage, minVoltage = 4.1, 3.85, 3.5
	v := float64(volts)
	var percentage float64
	if v >= maxVoltage {
		percentage = 100
	} else if v >= midVoltage {
		scaled := (v - midVoltage) / (maxVoltage - midVoltage)
		percentage = 80 + 20*math.Log10(1+scaled*9)
	} else if v >= minVoltage {
		scaled := (v - minVoltage) / (midVoltage - minVoltage)
		percentage = 80 * math.Log10(1+scaled*9)
	} else if v == 0 {
		// no voltage reported (bot booted off charger); the volts > 0 gate
		// in bwPollBot keeps this from ever triggering a go-home
		percentage = 70
	} else {
		percentage = 0
	}
	percentage = math.Round(percentage)
	if percentage < 0 {
		percentage = 0
	}
	if percentage > 100 {
		percentage = 100
	}
	return int(percentage)
}

func bwThreshold() int {
	if vars.APIConfig.Battery.GoHomePercent == nil {
		return 0
	}
	return *vars.APIConfig.Battery.GoHomePercent
}

func bwRobot(esn string) (*vector.Vector, error) {
	var ip string
	for _, bot := range vars.BotInfo.Robots {
		if bot.Esn == esn {
			ip = bot.IPAddress
			break
		}
	}
	bwMu.Lock()
	cached := bwConns[esn]
	bwMu.Unlock()
	if cached != nil && cached.ip == ip {
		return cached.robot, nil
	}
	robot, err := vars.GetRobot(esn)
	if err != nil {
		return nil, err
	}
	bwMu.Lock()
	bwConns[esn] = &bwCachedConn{robot: robot, ip: ip}
	bwMu.Unlock()
	return robot, nil
}

func bwDropConn(esn string) {
	bwMu.Lock()
	delete(bwConns, esn)
	bwMu.Unlock()
}

func BatteryWatchdog() {
	logger.Info("sdkapp", "", "battery go-home watchdog started")
	for {
		time.Sleep(bwPollInterval)
		for _, status := range GetConnectionStatus() {
			if status.Status != "online" {
				continue
			}
			bwSafePoll(status.Esn)
		}
	}
}

func bwSafePoll(esn string) {
	defer func() {
		if r := recover(); r != nil {
			logger.Warn("sdkapp", esn, fmt.Sprint("battery watchdog: recovered from panic: ", r))
		}
	}()
	bwPollBot(esn)
}

func bwPollBot(esn string) {
	bwMu.Lock()
	state := bwStates[esn]
	if state == nil {
		state = &bwBotState{}
		bwStates[esn] = state
	}
	if state.docking {
		bwMu.Unlock()
		return
	}
	cooling := time.Now().Before(state.coolingUntil)
	bwMu.Unlock()

	robot, err := bwRobot(esn)
	if err != nil {
		logger.Debug("sdkapp", esn, "battery watchdog: "+err.Error())
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), bwRPCTimeout)
	resp, err := robot.Conn.BatteryState(ctx, &vectorpb.BatteryStateRequest{})
	cancel()
	if err != nil {
		// transient errors don't touch the counters
		bwDropConn(esn)
		logger.Debug("sdkapp", esn, "battery watchdog: battery state: "+err.Error())
		return
	}

	volts := resp.BatteryVolts
	percent := bwBatteryPercent(volts)
	threshold := bwThreshold()
	home := resp.IsCharging || resp.IsOnChargerPlatform

	bwMu.Lock()
	if state.haveReading && home != state.lastHome {
		if home {
			logger.Info("sdkapp", esn, fmt.Sprintf("robot is back on the charger (%d%%, %.2fV)", percent, volts))
		} else {
			logger.Info("sdkapp", esn, fmt.Sprintf("robot left the charger (%d%%, %.2fV)", percent, volts))
		}
	}
	state.haveReading = true
	state.lastHome = home
	if home || volts <= 0 {
		state.consecutiveLow = 0
		state.attempts = 0
		state.coolingUntil = time.Time{}
		bwMu.Unlock()
		return
	}
	if cooling || threshold <= 0 {
		state.consecutiveLow = 0
		bwMu.Unlock()
		return
	}
	if percent <= threshold {
		state.consecutiveLow++
	} else {
		state.consecutiveLow = 0
	}
	if state.consecutiveLow < bwHysteresisN {
		bwMu.Unlock()
		return
	}
	state.consecutiveLow = 0
	if state.attempts >= bwMaxAttempts {
		state.attempts = 0
		state.coolingUntil = time.Now().Add(bwGiveUpCooldown)
		bwMu.Unlock()
		logger.Warn("sdkapp", esn, "battery watchdog: charger not reached after repeated attempts, backing off")
		return
	}
	state.attempts++
	state.docking = true
	bwMu.Unlock()

	logger.Warn("sdkapp", esn, fmt.Sprintf("battery low (%d%% <= %d%%, %.2fV), sending robot to charger", percent, threshold, volts))
	reached := bwDriveHome(robot, esn)

	bwMu.Lock()
	state.docking = false
	state.coolingUntil = time.Now().Add(bwCooldown)
	if reached {
		state.attempts = 0
	}
	bwMu.Unlock()
}

func bwDriveHome(robot *vector.Vector, esn string) bool {
	ctx, cancel := context.WithTimeout(context.Background(), bwDockTimeout)
	defer cancel()
	stream, err := robot.Conn.BehaviorControl(ctx)
	if err != nil {
		logger.Warn("sdkapp", esn, "battery watchdog: behavior control: "+err.Error())
		return false
	}
	err = stream.Send(&vectorpb.BehaviorControlRequest{
		RequestType: &vectorpb.BehaviorControlRequest_ControlRequest{
			ControlRequest: &vectorpb.ControlRequest{
				Priority: vectorpb.ControlRequest_OVERRIDE_BEHAVIORS,
			},
		},
	})
	if err != nil {
		logger.Warn("sdkapp", esn, "battery watchdog: control request: "+err.Error())
		return false
	}
	for {
		ctrlresp, err := stream.Recv()
		if err != nil {
			logger.Warn("sdkapp", esn, "battery watchdog: control grant: "+err.Error())
			return false
		}
		if ctrlresp.GetControlGrantedResponse() != nil {
			break
		}
	}
	// blocks until the dock behavior finishes or ctx expires
	_, err = robot.Conn.DriveOnCharger(ctx, &vectorpb.DriveOnChargerRequest{})
	stream.Send(&vectorpb.BehaviorControlRequest{
		RequestType: &vectorpb.BehaviorControlRequest_ControlRelease{
			ControlRelease: &vectorpb.ControlRelease{},
		},
	})
	if err != nil {
		logger.Warn("sdkapp", esn, "battery watchdog: drive on charger: "+err.Error())
		return false
	}
	vctx, vcancel := context.WithTimeout(context.Background(), bwRPCTimeout)
	defer vcancel()
	verify, err := robot.Conn.BatteryState(vctx, &vectorpb.BatteryStateRequest{})
	if err == nil && verify.IsOnChargerPlatform {
		logger.Info("sdkapp", esn, "battery watchdog: robot reached the charger")
		return true
	}
	logger.Warn("sdkapp", esn, "battery watchdog: robot did not reach the charger")
	return false
}

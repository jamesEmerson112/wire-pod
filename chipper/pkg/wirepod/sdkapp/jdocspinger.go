package sdkapp

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/zeroconf"
)

var JdocsPingerBots struct {
	mu     sync.Mutex
	Robots []JdocsPingerRobot
}

type JdocsPingerRobot struct {
	ESN                string `json:"esn"`
	GUID               string `json:"guid"`
	IP                 string `json:"ip"`
	TimeSinceLastCheck int    `json:"timesince"`
	Stopped            bool   `json:"stopped"`
}

type BotStatus struct {
	Esn       string `json:"esn"`
	IP        string `json:"ip"`
	Status    string `json:"status"`
	TimeSince int    `json:"timesince"`
}

func GetConnectionStatus() []BotStatus {
	JdocsPingerBots.mu.Lock()
	defer JdocsPingerBots.mu.Unlock()
	var statuses []BotStatus
	for _, robot := range vars.BotInfo.Robots {
		status := BotStatus{
			Esn:       robot.Esn,
			IP:        robot.IPAddress,
			Status:    "disconnected",
			TimeSince: -1,
		}
		for _, bot := range JdocsPingerBots.Robots {
			if bot.ESN == robot.Esn {
				status.TimeSince = bot.TimeSinceLastCheck
				if !bot.Stopped && bot.TimeSinceLastCheck <= 15 {
					status.Status = "online"
				} else if bot.Stopped && bot.TimeSinceLastCheck < 120 {
					status.Status = "offline"
				} else {
					status.Status = "disconnected"
				}
				break
			}
		}
		statuses = append(statuses, status)
	}
	return statuses
}

// the escape pod CA cert only gets appended to the cert store when a jdocs connection is created
// this doesn't happen at every boot
// this utilizes Vector's connCheck to see if a bot has disconnected from the server for more than 10 seconds
// if it has, it will pull jdocs from the bot which will cause the CA cert to get appended to the store

// setting JDOCS_PINGER_ENABLED=false will disable jdocs pinger
var PingerEnabled bool = true

func pingJdocs(target string) {
	ctx := context.Background()
	target = strings.Split(target, ":")[0]
	var serial string
	matched := false
	for _, robot := range vars.BotInfo.Robots {
		if strings.TrimSpace(strings.ToLower(robot.IPAddress)) == strings.TrimSpace(strings.ToLower(target)) {
			matched = true
			serial = robot.Esn
		}
	}
	if !matched {
		logger.Error("sdkapp", "", "jdocs pinger: serial not in bot json")
		return
	}
	robotTmp, err := NewWP(serial, false)
	if err != nil {
		logger.Error("sdkapp", serial, "error pinging jdocs: "+err.Error())
		return
	}
	_, err = robotTmp.Conn.BatteryState(ctx, &vectorpb.BatteryStateRequest{})
	if err != nil {
		robotTmp, err = NewWP(serial, true)
		if err != nil {
			logger.Error("sdkapp", serial, "error pinging jdocs: "+err.Error())
			return
		}
		_, err = robotTmp.Conn.BatteryState(ctx, &vectorpb.BatteryStateRequest{})
		if err != nil {
			logger.Error("sdkapp", serial, "ping failed, likely unauthenticated")
			return
		}
	}
	resp, err := robotTmp.Conn.PullJdocs(ctx, &vectorpb.PullJdocsRequest{
		JdocTypes: []vectorpb.JdocType{vectorpb.JdocType_ROBOT_SETTINGS},
	})
	if err != nil {
		logger.Error("sdkapp", serial, "pull jdocs: "+err.Error())
		return
	}
	logger.Info("sdkapp", serial, "pulled jdocs")
	// write to file
	var jdoc vars.AJdoc
	jdoc.DocVersion = resp.NamedJdocs[0].Doc.DocVersion
	jdoc.FmtVersion = resp.NamedJdocs[0].Doc.FmtVersion
	jdoc.ClientMetadata = resp.NamedJdocs[0].Doc.ClientMetadata
	jdoc.JsonDoc = resp.NamedJdocs[0].Doc.JsonDoc
	vars.AddJdoc("vic:"+serial, "vic.RobotSettings", jdoc)
}

func InitJdocsPinger() {
	if os.Getenv("JDOCS_PINGER_ENABLED") == "false" {
		logger.Debug("sdkapp", "", "jdocs pinger disabled (JDOCS_PINGER_ENABLED=false)")
		PingerEnabled = false
		return
	}
	fmt.Println("Starting jdocs pinger ticker")
	go func() {
		for {
			JdocsPingerBots.mu.Lock()
			for i, bot := range JdocsPingerBots.Robots {
				JdocsPingerBots.Robots[i].TimeSinceLastCheck = JdocsPingerBots.Robots[i].TimeSinceLastCheck + 1
				if JdocsPingerBots.Robots[i].TimeSinceLastCheck > 15 && !bot.Stopped {
					logger.Debug("sdkapp", bot.ESN, "no conn check in 15s, will ping jdocs on next check")
					JdocsPingerBots.Robots[i].Stopped = true
				}
			}
			JdocsPingerBots.mu.Unlock()
			time.Sleep(time.Second)
		}
	}()
}

func ShouldPingJdocs(target string) bool {
	var esn, guid, botip string
	matched := false
	for _, bot := range vars.BotInfo.Robots {
		if target == bot.IPAddress {
			esn = bot.Esn
			guid = bot.GUID
			botip = bot.IPAddress
			matched = true
			break
		}
	}
	if !matched {
		return false
	}
	JdocsPingerBots.mu.Lock()
	defer JdocsPingerBots.mu.Unlock()
	for i, bot := range JdocsPingerBots.Robots {
		if esn == bot.ESN {
			if bot.Stopped {
				JdocsPingerBots.Robots[i].TimeSinceLastCheck = 0
				JdocsPingerBots.Robots[i].Stopped = false
				return true
			} else {
				JdocsPingerBots.Robots[i].TimeSinceLastCheck = 0
				return false
			}
		}
	}
	// below will only execute if esn doesn't match bots in list
	newBot := JdocsPingerRobot{
		ESN:                esn,
		GUID:               guid,
		IP:                 botip,
		TimeSinceLastCheck: 0,
		Stopped:            false,
	}
	JdocsPingerBots.Robots = append(JdocsPingerBots.Robots, newBot)
	return true
}

func connCheck(w http.ResponseWriter, r *http.Request) {
	switch {
	default:
		http.Error(w, "not found", http.StatusNotFound)
		return
	case strings.Contains(r.URL.Path, "/ok"):
		if r.FormValue("runMDNS") == "true" {
			RunMDNS("t")
			fmt.Fprintf(w, "ran")
			return
		}
		if PingerEnabled {
			//logger.Println("connCheck request from " + r.RemoteAddr)
			robotTarget := strings.Split(r.RemoteAddr, ":")[0]
			jsonB, _ := json.Marshal(vars.BotInfo)
			json := string(jsonB)
			if strings.Contains(json, strings.TrimSpace(robotTarget)) {
				ping := ShouldPingJdocs(robotTarget)
				if ping {
					pingJdocs(robotTarget)
				}
			} else {
				go RunMDNS(strings.Split(r.RemoteAddr, ":")[0])
			}
		}
		fmt.Fprintf(w, "ok")
		return
	}
}

var MDNSAlreadyRun []string

var RunningMDNS bool

func RunMDNS(botIP string) {
	for _, ip := range MDNSAlreadyRun {
		if ip == botIP {
			return
		}
	}
	fmt.Println("Running mDNS...")
	ctx, cancel := context.WithTimeout(context.Background(), time.Second*5)
	defer cancel()
	entries := make(chan *zeroconf.ServiceEntry)
	resolver, _ := zeroconf.NewResolver()
	resolver.Browse(ctx, "_ankivector._tcp", "local", entries)
	for entry := range entries {
		robotID := strings.Split(entry.HostName, ".")[0]
		matched := false
		for _, rinf := range vars.RecurringInfo {
			if rinf.ID == robotID {
				vars.AddToRInfo(rinf.ESN, robotID, fmt.Sprint(entry.AddrIPv4[0]))
				for i, rob := range vars.BotInfo.Robots {
					if rob.Esn == rinf.ESN {
						vars.BotInfo.Robots[i].IPAddress = fmt.Sprint(entry.AddrIPv4[0])
						jsonBytes, _ := json.Marshal(vars.BotInfo)
						fmt.Println("Updating robot " + robotID)
						go os.WriteFile(vars.BotInfoPath, jsonBytes, 0777)
					}
				}
				go func() {
					// wait for escapepod.local trasmit
					if vars.APIConfig.Server.EPConfig {
						time.Sleep(time.Second)
					}
					pingJdocs(fmt.Sprint(entry.AddrIPv4[0]))
				}()
				matched = true
				break
			}
		}
		if !matched {
			MDNSAlreadyRun = append(MDNSAlreadyRun, botIP)
		}
	}
	fmt.Println("Done running mDNS")
}

package wirepod_ttr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os/exec"
	"strings"

	pb "github.com/digital-dream-labs/api/go/chipperpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/scripting"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/vtt"
)

type systemIntentResponseStruct struct {
	Status       string `json:"status"`
	ReturnIntent string `json:"returnIntent"`
}

func IntentPass(req interface{}, intentThing string, speechText string, intentParams map[string]string, isParam bool) (interface{}, error) {
	var esn string
	var req1 *vtt.IntentRequest
	var req2 *vtt.IntentGraphRequest
	var isIntentGraph bool
	if str, ok := req.(*vtt.IntentRequest); ok {
		req1 = str
		esn = req1.Device
		isIntentGraph = false
	} else if str, ok := req.(*vtt.IntentGraphRequest); ok {
		req2 = str
		esn = req2.Device
		isIntentGraph = true
	}

	// intercept if not intent graph but intent graph is enabled
	if !isIntentGraph && vars.APIConfig.Knowledge.IntentGraph && intentThing == "intent_system_unmatched" {
		intentThing = "intent_greeting_hello"
	}

	var intentResult pb.IntentResult
	if isParam {
		intentResult = pb.IntentResult{
			QueryText:  speechText,
			Action:     intentThing,
			Parameters: intentParams,
		}
	} else {
		intentResult = pb.IntentResult{
			QueryText: speechText,
			Action:    intentThing,
		}
	}
	logger.Info("intent", esn, "matched "+intentThing+", text '"+speechText+"'")
	if isParam {
		logger.Info("intent", esn, "params "+fmt.Sprint(intentParams))
	}
	intent := pb.IntentResponse{
		IsFinal:      true,
		IntentResult: &intentResult,
	}
	intentGraphSend := pb.IntentGraphResponse{
		ResponseType: pb.IntentGraphMode_INTENT,
		IsFinal:      true,
		IntentResult: &intentResult,
		CommandType:  pb.RobotMode_VOICE_COMMAND.String(),
	}
	if !isIntentGraph {
		if err := req1.Stream.Send(&intent); err != nil {
			return nil, err
		}
		r := &vtt.IntentResponse{
			Intent: &intent,
		}
		logger.Info("intent", esn, "intent sent: "+intentThing)
		if isParam {
			logger.Debug("intent", esn, "params sent: "+fmt.Sprint(intentParams))
		} else {
			logger.Debug("intent", esn, "no params sent")
		}
		return r, nil
	} else {
		if err := req2.Stream.Send(&intentGraphSend); err != nil {
			return nil, err
		}
		r := &vtt.IntentGraphResponse{
			Intent: &intentGraphSend,
		}
		logger.Info("intent", esn, "intent sent: "+intentThing)
		if isParam {
			logger.Debug("intent", esn, "params sent: "+fmt.Sprint(intentParams))
		} else {
			logger.Debug("intent", esn, "no params sent")
		}
		return r, nil
	}
}

func customIntentHandler(req interface{}, voiceText string, botSerial string) bool {
	var successMatched bool = false
	if vars.CustomIntentsExist {
		for _, c := range vars.CustomIntents {
			for _, v := range c.Utterances {
				//if strings.Contains(voiceText, strings.ToLower(strings.TrimSpace(v))) {
				// Check whether the custom sentence is either at the end of the spoken text or space-separated...
				var seekText = strings.ToLower(strings.TrimSpace(v))
				// System intents can also match any utterances (*)
				if (c.IsSystemIntent && strings.HasPrefix(seekText, "*")) || strings.Contains(voiceText, seekText) {
					logger.Debug("intent", botSerial, "custom intent matched: "+c.Name+" - "+c.Description+" - "+c.Intent)
					var intentParams map[string]string
					var isParam bool = false
					if c.Params.ParamValue != "" {
						logger.Debug("intent", botSerial, "custom intent parameter: "+c.Params.ParamName+" - "+c.Params.ParamValue)
						intentParams = map[string]string{c.Params.ParamName: c.Params.ParamValue}
						isParam = true
					}

					go func() {
						if c.LuaScript != "" {
							err := scripting.RunLuaScript(botSerial, c.LuaScript)
							if err != nil {
								logger.Error("lua", botSerial, "lua script: "+err.Error())
							}
						}
					}()

					var args []string
					for _, arg := range c.ExecArgs {
						switch arg {
						case "!botSerial":
							arg = botSerial
						case "!speechText":
							arg = "\"" + voiceText + "\""
						case "!intentName":
							arg = c.Name
						case "!locale":
							arg = vars.APIConfig.STT.Language
						}
						args = append(args, arg)
					}
					var customIntentExec *exec.Cmd
					if len(args) == 0 {
						logger.Debug("intent", botSerial, "executing: "+c.Exec)
						customIntentExec = exec.Command(c.Exec)
					} else {
						logger.Debug("intent", botSerial, "executing: "+c.Exec+" "+strings.Join(args, " "))
						customIntentExec = exec.Command(c.Exec, args...)
					}
					var out bytes.Buffer
					var stderr bytes.Buffer
					customIntentExec.Stdout = &out
					customIntentExec.Stderr = &stderr
					err := customIntentExec.Run()
					if err != nil {
						fmt.Println(fmt.Sprint(err) + ": " + stderr.String())
					}
					logger.Debug("intent", botSerial, "custom intent exec output: "+strings.TrimSpace(string(out.String())))

					if c.IsSystemIntent {
						// A system intent returns its output in json format
						var resp systemIntentResponseStruct
						err := json.Unmarshal(out.Bytes(), &resp)
						if err == nil && resp.Status == "ok" {
							logger.Debug("intent", botSerial, "system intent parsed and executed successfully")
							IntentPass(req, resp.ReturnIntent, voiceText, intentParams, isParam)
							successMatched = true
						}
					} else {
						IntentPass(req, c.Intent, voiceText, intentParams, isParam)
						successMatched = true
					}
					break
				}
				if successMatched {
					break
				}
			}
			if successMatched {
				break
			}
		}
	}
	return successMatched
}

func pluginFunctionHandler(req interface{}, voiceText string, botSerial string) bool {
	matched := false
	var intent string
	var igr *vtt.IntentGraphRequest
	if str, ok := req.(*vtt.IntentGraphRequest); ok {
		igr = str
	}
	var pluginResponse string
	for num, array := range PluginUtterances {
		array := array
		for _, str := range *array {
			if strings.Contains(voiceText, str) || str == "*" {
				logger.Debug("intent", botSerial, "matched plugin "+PluginNames[num]+", executing function")
				var guid string
				var target string
				for _, bot := range vars.BotInfo.Robots {
					if bot.Esn == botSerial {
						guid = bot.GUID
						target = bot.IPAddress + ":443"
					}
				}
				intent, pluginResponse = PluginFunctions[num](voiceText, botSerial, guid, target)
				if intent == "" && pluginResponse == "" {
					break
				}
				if intent == "" {
					intent = "intent_imperative_praise"
				}
				logger.Debug("intent", botSerial, "plugin "+PluginNames[num]+", response "+pluginResponse)
				if pluginResponse != "" && igr != nil {
					response := &pb.IntentGraphResponse{
						Session:      igr.Session,
						DeviceId:     igr.Device,
						ResponseType: pb.IntentGraphMode_KNOWLEDGE_GRAPH,
						SpokenText:   pluginResponse,
						QueryText:    voiceText,
						IsFinal:      true,
					}
					igr.Stream.Send(response)
				} else if pluginResponse != "" {
					KGSim(botSerial, pluginResponse)
				} else {
					IntentPass(req, intent, voiceText, make(map[string]string), false)
				}
				matched = true
				break
			}
		}
		if matched {
			break
		}
	}
	return matched
}

func ProcessTextAll(req interface{}, voiceText string, intents []vars.JsonIntent, isOpus bool) bool {
	var botSerial string
	var req2 *vtt.IntentRequest
	var req1 *vtt.KnowledgeGraphRequest
	var req3 *vtt.IntentGraphRequest
	if str, ok := req.(*vtt.IntentRequest); ok {
		req2 = str
		botSerial = req2.Device
	} else if str, ok := req.(*vtt.KnowledgeGraphRequest); ok {
		req1 = str
		botSerial = req1.Device
	} else if str, ok := req.(*vtt.IntentGraphRequest); ok {
		req3 = str
		botSerial = req3.Device
	}
	var matched int = 0
	var intentNum int = 0
	var successMatched bool = false
	voiceText = strings.ToLower(voiceText)
	pluginMatched := pluginFunctionHandler(req, voiceText, botSerial)
	customIntentMatched := customIntentHandler(req, voiceText, botSerial)
	if !customIntentMatched && !pluginMatched {
		logger.Debug("intent", botSerial, "not a custom intent")
		// Look for a perfect match first
		for _, b := range intents {
			for _, c := range b.Keyphrases {
				if voiceText == strings.ToLower(c) {
					logger.Debug("intent", botSerial, "perfect match for intent "+b.Name+" ("+strings.ToLower(c)+")")
					if isOpus {
						ParamChecker(req, b.Name, voiceText, botSerial)
					} else {
						prehistoricParamChecker(req, b.Name, voiceText)
					}
					successMatched = true
					matched = 1
					break
				}
			}
			if matched == 1 {
				matched = 0
				break
			}
			intentNum = intentNum + 1
		}
		// Not found? Then let's be happy with a bare substring search
		if !successMatched {
			intentNum = 0
			matched = 0
			for _, b := range intents {
				for _, c := range b.Keyphrases {
					if strings.Contains(voiceText, strings.ToLower(c)) && !b.RequireExactMatch {
						logger.Debug("intent", botSerial, "partial match for intent "+b.Name+" ("+strings.ToLower(c)+")")
						if isOpus {
							ParamChecker(req, b.Name, voiceText, botSerial)
						} else {
							prehistoricParamChecker(req, b.Name, voiceText)
						}
						successMatched = true
						matched = 1
						break
					}
				}
				if matched == 1 {
					matched = 0
					break
				}
				intentNum = intentNum + 1
			}
		}
	} else {
		logger.Debug("intent", botSerial, "this is a custom intent or plugin")
		successMatched = true
	}
	return successMatched
}

func KnowledgeGraphResponseIG(req *vtt.IntentGraphRequest, spokenText string, queryText string) error {
	intentResult := pb.IntentResult{
		QueryText: queryText,
		Action:    "intent_knowledge_response_extend_bypass",
	}

	intentGraphSend := pb.IntentGraphResponse{
		ResponseType: pb.IntentGraphMode_KNOWLEDGE_GRAPH,
		IsFinal:      true,
		IntentResult: &intentResult,
		SpokenText:   spokenText,
		QueryText:    queryText,
		CommandType:  pb.RobotMode_VOICE_COMMAND.String(),
	}
	
	if err := req.Stream.Send(&intentGraphSend); err != nil {
		return err
	}
	return nil
}

package processreqs

import (
	"strings"

	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/kercre123/wire-pod/chipper/pkg/vtt"
	sr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/speechrequest"
	ttr "github.com/kercre123/wire-pod/chipper/pkg/wirepod/ttr"
)

func (s *Server) ProcessIntentGraph(req *vtt.IntentGraphRequest) (*vtt.IntentGraphResponse, error) {
	var successMatched bool
	speechReq := sr.ReqToSpeechRequest(req)
	var transcribedText string
	if !isSti {
		var err error
		transcribedText, err = sttHandler(speechReq)
		if err != nil {
			ttr.IntentPass(req, "intent_system_noaudio", "voice processing error: "+err.Error(), map[string]string{"error": err.Error()}, true)
			return nil, nil
		}
		if strings.TrimSpace(transcribedText) == "" {
			ttr.IntentPass(req, "intent_system_noaudio", "", map[string]string{}, false)
			return nil, nil
		}
		successMatched = ttr.ProcessTextAll(req, transcribedText, vars.IntentList, speechReq.IsOpus)
	} else {
		intent, slots, err := stiHandler(speechReq)
		if err != nil {
			if err.Error() == "inference not understood" {
				logger.Debug("intent", req.Device, "no intent matched")
				ttr.IntentPass(req, "intent_system_unmatched", "voice processing error", map[string]string{"error": err.Error()}, true)
				return nil, nil
			}
			logger.Println(err)
			ttr.IntentPass(req, "intent_system_noaudio", "voice processing error", map[string]string{"error": err.Error()}, true)
			return nil, nil
		}
		ttr.ParamCheckerSlotsEnUS(req, intent, slots, speechReq.IsOpus, speechReq.Device)
		return nil, nil
	}
	// if !successMatched {
	// 	logger.Println("No intent was matched.")
	// 	if vars.APIConfig.Knowledge.Enable && vars.APIConfig.Knowledge.Provider == "openai" && len([]rune(transcribedText)) >= 8 {
	// 		apiResponse := openaiRequest(transcribedText)
	// 		response := &pb.IntentGraphResponse{
	// 			Session:      req.Session,
	// 			DeviceId:     req.Device,
	// 			ResponseType: pb.IntentGraphMode_KNOWLEDGE_GRAPH,
	// 			SpokenText:   apiResponse,
	// 			QueryText:    transcribedText,
	// 			IsFinal:      true,
	// 		}
	// 		req.Stream.Send(response)
	// 		return nil, nil
	// 	}
	// 	ttr.IntentPass(req, "intent_system_unmatched", transcribedText, map[string]string{"": ""}, false)
	// 	return nil, nil
	// }
	if !successMatched {
		if vars.APIConfig.Knowledge.IntentGraph && vars.APIConfig.Knowledge.Enable {
			if vars.APIConfig.Knowledge.Provider == "houndify" {
				if len([]rune(transcribedText)) >= 8 {
					logger.Debug("llm", req.Device, "forwarding to houndify")
					InitKnowledge() // Errors without this for whatever reason even though I think it should be inited already
					apiResponse := houndifyTextRequest(transcribedText, req.Device, req.Session)
					if apiResponse != "" && !strings.Contains(apiResponse, "not enabled") && !strings.Contains(apiResponse, "Knowledge graph is not enabled") && !strings.Contains(apiResponse, "Didn't get that!") {
						ttr.KnowledgeGraphResponseIG(req, apiResponse, transcribedText)
						logger.Info("intent", req.Device, "request served via houndify")
						return nil, nil
					}
					// If Houndify fails or returns nothing useful, fall through to unmatched
					logger.Debug("llm", req.Device, "houndify returned empty or error response")
				}
			} else {
				logger.Debug("llm", req.Device, "making LLM request")
				_, err := ttr.StreamingKGSim(req, req.Device, transcribedText, false)
				if err != nil {
					logger.Error("llm", req.Device, "LLM error: "+err.Error())
					ttr.IntentPass(req, "intent_system_unmatched", transcribedText, map[string]string{"": ""}, false)
					ttr.KGSim(req.Device, "There was an error getting a response from the L L M. Check the logs in the web interface.")
				}
				logger.Info("intent", req.Device, "request served")
				return nil, nil
			}
		}
		logger.Debug("intent", req.Device, "no intent matched")
		ttr.IntentPass(req, "intent_system_unmatched", transcribedText, map[string]string{"": ""}, false)
		return nil, nil
	}
	logger.Info("intent", req.Device, "request served")
	return nil, nil
}

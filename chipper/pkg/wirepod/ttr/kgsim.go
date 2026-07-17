package wirepod_ttr

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log"
	"regexp"
	"strings"
	"time"
	"unicode"

	"golang.org/x/text/transform"
	"golang.org/x/text/unicode/norm"

	"github.com/fforchino/vector-go-sdk/pkg/vector"
	"github.com/fforchino/vector-go-sdk/pkg/vectorpb"
	"github.com/kercre123/wire-pod/chipper/pkg/logger"
	"github.com/kercre123/wire-pod/chipper/pkg/vars"
	"github.com/sashabaranov/go-openai"
)

func GetChat(esn string) vars.RememberedChat {
	for _, chat := range vars.RememberedChats {
		if chat.ESN == esn {
			return chat
		}
	}
	return vars.RememberedChat{
		ESN: esn,
	}
}

func PlaceChat(chat vars.RememberedChat) {
	for i, achat := range vars.RememberedChats {
		if achat.ESN == chat.ESN {
			vars.RememberedChats[i] = chat
			return
		}
	}
	vars.RememberedChats = append(vars.RememberedChats, chat)
}

// remember last 16 lines of chat
func Remember(user, ai openai.ChatCompletionMessage, esn string) {
	chatAppend := []openai.ChatCompletionMessage{
		user,
		ai,
	}
	currentChat := GetChat(esn)
	if len(currentChat.Chats) == 16 {
		var newChat vars.RememberedChat
		newChat.ESN = currentChat.ESN
		for i, chat := range currentChat.Chats {
			if i < 2 {
				continue
			}
			newChat.Chats = append(newChat.Chats, chat)
		}
		currentChat = newChat
	}
	currentChat.ESN = esn
	currentChat.Chats = append(currentChat.Chats, chatAppend...)
	PlaceChat(currentChat)
}

func isMn(r rune) bool {
	// Remove the characters that are not related to Vietnamese.
	// Retain the tonal marks and diacritics such as the circumflex, ơ, and ư in Vietnamese.
	keepMarks := []rune{
		'\u0300', // Dấu huyền
		'\u0301', // Dấu sắc
		'\u0303', // Dấu ngã
		'\u0309', // Dấu hỏi
		'\u0323', // Dấu nặng
		'\u0302', // Dấu mũ (â, ê, ô)
		'\u031B', // Dấu ơ và ư
		'\u0306', // Dấu trầm
	}
	if unicode.Is(unicode.Mn, r) {
		for _, mark := range keepMarks {
			if r == mark {
				return false
			}
		}
		return true
	}
	return false
}

func removeSpecialCharacters(str string) string {

	// these two lines create a transformation that decomposes characters, removes non-spacing marks (like diacritics), and then recomposes the characters, effectively removing special characters
	t := transform.Chain(norm.NFD, transform.RemoveFunc(isMn), norm.NFC)
	result, _, _ := transform.String(t, str)

	// Define the regular expression to match special characters
	re := regexp.MustCompile(`[&^*#@]`)

	// Replace special characters with an empty string
	result = removeEmojis(re.ReplaceAllString(result, ""))

	// Replace special characters with ASCII
	// * COPY/PASTE TO ADD MORE CHARACTERS:
	//   result = strings.ReplaceAll(result, "", "")
	result = strings.ReplaceAll(result, "‘", "'")
	result = strings.ReplaceAll(result, "’", "'")
	result = strings.ReplaceAll(result, "“", "\"")
	result = strings.ReplaceAll(result, "”", "\"")
	result = strings.ReplaceAll(result, "—", "-")
	result = strings.ReplaceAll(result, "–", "-")
	result = strings.ReplaceAll(result, "…", "...")
	result = strings.ReplaceAll(result, "\u00A0", " ")
	result = strings.ReplaceAll(result, "•", "*")
	result = strings.ReplaceAll(result, "¼", "1/4")
	result = strings.ReplaceAll(result, "½", "1/2")
	result = strings.ReplaceAll(result, "¾", "3/4")
	result = strings.ReplaceAll(result, "×", "x")
	result = strings.ReplaceAll(result, "÷", "/")
	result = strings.ReplaceAll(result, "ç", "c")
	result = strings.ReplaceAll(result, "©", "(c)")
	result = strings.ReplaceAll(result, "®", "(r)")
	result = strings.ReplaceAll(result, "™", "(tm)")
	result = strings.ReplaceAll(result, "@", "(a)")
	result = strings.ReplaceAll(result, " AI ", " A. I. ")
	return result
}

func removeEmojis(input string) string {
	// a mess, but it works!
	re := regexp.MustCompile(`[\x{1F600}-\x{1F64F}]|[\x{1F300}-\x{1F5FF}]|[\x{1F680}-\x{1F6FF}]|[\x{1F1E0}-\x{1F1FF}]|[\x{2600}-\x{26FF}]|[\x{2700}-\x{27BF}]|[\x{1F900}-\x{1F9FF}]|[\x{1F004}]|[\x{1F0CF}]|[\x{1F18E}]|[\x{1F191}-\x{1F251}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]|[\x{1F004}-\x{1F0CF}]|[\x{1F191}-\x{1F251}]|[\x{2B50}]`)
	result := re.ReplaceAllString(input, "")
	return result
}

// default model for the openai provider: cheapest current-gen reasoning tier
const defaultOpenAIModel = "gpt-5.6-luna"

// gpt-5* and o-series models reject max_tokens and sampling params like
// temperature; they take max_completion_tokens and a reasoning effort instead
func isReasoningModel(model string) bool {
	for _, prefix := range []string{"gpt-5", "o1", "o3", "o4"} {
		if strings.HasPrefix(model, prefix) {
			return true
		}
	}
	return false
}

// sets the token/sampling/reasoning params on a request to match its Model;
// clears both param groups first so it is safe to re-apply after a model swap
func setAIReqParams(aireq *openai.ChatCompletionRequest) {
	aireq.MaxTokens = 0
	aireq.MaxCompletionTokens = 0
	aireq.Temperature = 0
	aireq.TopP = 0
	aireq.ReasoningEffort = ""
	if isReasoningModel(aireq.Model) {
		aireq.MaxCompletionTokens = 2048
		effort := strings.TrimSpace(vars.APIConfig.Knowledge.ReasoningEffort)
		if effort == "" {
			effort = "medium"
		}
		aireq.ReasoningEffort = effort
	} else {
		aireq.MaxTokens = 2048
		aireq.Temperature = 1
		aireq.TopP = 1
	}
}

func CreateAIReq(transcribedText, esn string, gpt3tryagain, isKG bool) openai.ChatCompletionRequest {
	defaultPrompt := "You are a helpful, animated robot called Vector. Keep the response concise yet informative."

	var nChat []openai.ChatCompletionMessage

	smsg := openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleSystem,
	}
	if strings.TrimSpace(vars.APIConfig.Knowledge.OpenAIPrompt) != "" {
		smsg.Content = strings.TrimSpace(vars.APIConfig.Knowledge.OpenAIPrompt)
	} else {
		smsg.Content = defaultPrompt
	}

	var model string

	if gpt3tryagain {
		model = openai.GPT4oMini
	} else if vars.APIConfig.Knowledge.Provider == "openai" {
		model = strings.TrimSpace(vars.APIConfig.Knowledge.Model)
		if model == "" {
			model = defaultOpenAIModel
		}
		logger.Debug("llm", esn, "using "+model)
	} else {
		logger.Debug("llm", esn, "using "+vars.APIConfig.Knowledge.Model)
		model = vars.APIConfig.Knowledge.Model
	}

	smsg.Content = CreatePrompt(smsg.Content, model, isKG)

	nChat = append(nChat, smsg)
	if vars.APIConfig.Knowledge.SaveChat {
		rchat := GetChat(esn)
		logger.Debug("llm", esn, "using remembered chats, length of "+fmt.Sprint(len(rchat.Chats))+" messages")
		nChat = append(nChat, rchat.Chats...)
	}
	nChat = append(nChat, openai.ChatCompletionMessage{
		Role:    openai.ChatMessageRoleUser,
		Content: transcribedText,
	})

	aireq := openai.ChatCompletionRequest{
		Model:    model,
		Messages: nChat,
		Stream:   true,
	}
	setAIReqParams(&aireq)
	return aireq
}

func StreamingKGSim(req interface{}, esn string, transcribedText string, isKG bool) (string, error) {
	start := make(chan bool)
	stop := make(chan bool)
	stopStop := make(chan bool)
	kgReadyToAnswer := make(chan bool)
	kgStopLooping := false
	ctx := context.Background()
	matched := false
	var robot *vector.Vector
	var guid string
	var target string
	for _, bot := range vars.BotInfo.Robots {
		if esn == bot.Esn {
			guid = bot.GUID
			target = bot.IPAddress + ":443"
			matched = true
			break
		}
	}
	if matched {
		var err error
		robot, err = vector.New(vector.WithSerialNo(esn), vector.WithToken(guid), vector.WithTarget(target))
		if err != nil {
			return err.Error(), err
		}
	}
	_, err := robot.Conn.BatteryState(context.Background(), &vectorpb.BatteryStateRequest{})
	if err != nil {
		return "", err
	}
	if isKG {
		BControl(robot, ctx, start, stop)
		go func() {
			for {
				if kgStopLooping {
					kgReadyToAnswer <- true
					break
				}
				robot.Conn.PlayAnimation(ctx, &vectorpb.PlayAnimationRequest{
					Animation: &vectorpb.Animation{
						Name: "anim_knowledgegraph_searching_01",
					},
					Loops: 1,
				})
				time.Sleep(time.Second / 3)
			}
		}()
	}
	var fullRespText string
	var fullfullRespText string
	var fullRespSlice []string
	var isDone bool
	var lastFinishReason string
	var c *openai.Client
	switch vars.APIConfig.Knowledge.Provider {
	case "together":
		if vars.APIConfig.Knowledge.Model == "" {
			vars.APIConfig.Knowledge.Model = "meta-llama/Llama-3-70b-chat-hf"
			vars.WriteConfigToDisk()
		}
		conf := openai.DefaultConfig(vars.APIConfig.Knowledge.Key)
		conf.BaseURL = "https://api.together.xyz/v1"
		c = openai.NewClientWithConfig(conf)
	case "custom":
		conf := openai.DefaultConfig(vars.APIConfig.Knowledge.Key)
		conf.BaseURL = vars.APIConfig.Knowledge.Endpoint
		c = openai.NewClientWithConfig(conf)
	case "openai":
		c = openai.NewClient(vars.APIConfig.Knowledge.Key)
	}
	speakReady := make(chan string)
	successIntent := make(chan bool)

	aireq := CreateAIReq(transcribedText, esn, false, isKG)

	stream, err := c.CreateChatCompletionStream(ctx, aireq)
	if err != nil {
		log.Printf("Error creating chat completion stream: %v", err)
		if strings.Contains(err.Error(), "does not exist") && vars.APIConfig.Knowledge.Provider == "openai" {
			logger.Warn("llm", esn, aireq.Model+" not accessible with this key; check the OpenAI account")
			aireq := CreateAIReq(transcribedText, esn, true, isKG)
			logger.Warn("llm", esn, "falling back to "+aireq.Model)
			stream, err = c.CreateChatCompletionStream(ctx, aireq)
			if err != nil {
				logger.Error("llm", esn, "openai still not returning a response after falling back")
				return "", err
			}
		} else {
			if isKG {
				kgStopLooping = true
				for range kgReadyToAnswer {
					break
				}
				stop <- true
				time.Sleep(time.Second / 3)
				KGSim(esn, "There was an error getting data from the L. L. M.")
			}
			return "", err
		}
	}
	if !isKG {
		// answer the robot's intent request immediately so its firmware
		// doesn't time out while a reasoning model thinks; the actual
		// speech happens through the SDK once sentences arrive
		IntentPass(req, "intent_greeting_hello", transcribedText, map[string]string{}, false)
	}
	nChat := aireq.Messages
	nChat = append(nChat, openai.ChatCompletionMessage{
		Role: openai.ChatMessageRoleAssistant,
	})
	fmt.Println("LLM stream response: ")
	go func() {
		for {
			response, err := stream.Recv()
			if errors.Is(err, io.EOF) {
				if len(fullRespSlice) == 0 && strings.TrimSpace(fullRespText) != "" {
					// the whole answer had no sentence punctuation (e.g. a
					// one-word response); speak it as-is instead of dropping it
					logger.Info("llm", esn, "response has no sentence punctuation, using it as-is")
					fullRespSlice = append(fullRespSlice, strings.TrimSpace(fullRespText))
					successIntent <- true
				}
				// prevents a crash
				if len(fullRespSlice) == 0 {
					finishReason := lastFinishReason
					if finishReason == "" {
						finishReason = "unknown"
					}
					logger.Warn("llm", esn, "LLM stream ended with no usable response (finish reason: "+finishReason+")")
					successIntent <- false
					if isKG {
						kgStopLooping = true
						for range kgReadyToAnswer {
							break
						}
						stop <- true
						time.Sleep(time.Second / 3)
						KGSim(esn, "There was an error getting data from the L. L. M.")
					}
					break
				}
				isDone = true
				// if fullRespSlice != fullRespText, add that missing bit to fullRespSlice
				newStr := fullRespSlice[0]
				for i, str := range fullRespSlice {
					if i == 0 {
						continue
					}
					newStr = newStr + " " + str
				}
				if strings.TrimSpace(newStr) != strings.TrimSpace(fullfullRespText) {
					logger.Debug("llm", esn, "LLM debug: there is content after the last punctuation mark")
					extraBit := strings.TrimPrefix(fullRespText, newStr)
					fullRespSlice = append(fullRespSlice, extraBit)
				}
				if vars.APIConfig.Knowledge.SaveChat {
					Remember(openai.ChatCompletionMessage{
						Role:    openai.ChatMessageRoleUser,
						Content: transcribedText,
					},
						openai.ChatCompletionMessage{
							Role:    openai.ChatMessageRoleAssistant,
							Content: newStr,
						},
						esn)
				}
				logger.Info("llm", esn, "response: "+newStr)
				logger.Debug("llm", esn, "LLM stream finished")
				return
			}

			if err != nil {
				logger.Error("llm", esn, "stream error: "+err.Error())
				// unblock the caller and the speaking loop instead of hanging them
				isDone = true
				select {
				case successIntent <- false:
				default:
				}
				select {
				case speakReady <- "":
				default:
				}
				return
			}

			if len(response.Choices) == 0 {
				logger.Debug("llm", esn, "empty response chunk")
				continue
			}

			if response.Choices[0].FinishReason != "" {
				lastFinishReason = string(response.Choices[0].FinishReason)
			}
			fullfullRespText = fullfullRespText + removeSpecialCharacters(response.Choices[0].Delta.Content)
			fullRespText = fullRespText + removeSpecialCharacters(response.Choices[0].Delta.Content)
			if strings.Contains(fullRespText, "...") || strings.Contains(fullRespText, ".'") || strings.Contains(fullRespText, ".\"") || strings.Contains(fullRespText, ".") || strings.Contains(fullRespText, "?") || strings.Contains(fullRespText, "!") {
				var sepStr string
				if strings.Contains(fullRespText, "...") {
					sepStr = "..."
				} else if strings.Contains(fullRespText, ".'") {
					sepStr = ".'"
				} else if strings.Contains(fullRespText, ".\"") {
					sepStr = ".\""
				} else if strings.Contains(fullRespText, ".") {
					sepStr = "."
				} else if strings.Contains(fullRespText, "?") {
					sepStr = "?"
				} else if strings.Contains(fullRespText, "!") {
					sepStr = "!"
				}
				splitResp := strings.Split(strings.TrimSpace(fullRespText), sepStr)
				fullRespSlice = append(fullRespSlice, strings.TrimSpace(splitResp[0])+sepStr)
				fullRespText = splitResp[1]
				select {
				case successIntent <- true:
				default:
				}
				select {
				case speakReady <- strings.TrimSpace(splitResp[0]) + sepStr:
				default:
				}
			}
		}
	}()
	for is := range successIntent {
		if is {
			break
		} else {
			return "", errors.New("llm returned no response")
		}
	}
	time.Sleep(time.Millisecond * 200)
	if !isKG {
		BControl(robot, ctx, start, stop)
	}
	interrupted := false
	go func() {
		interrupted = InterruptKGSimWhenTouchedOrWaked(robot, stop, stopStop)
	}()
	var TTSLoopAnimation string
	var TTSGetinAnimation string
	if isKG {
		TTSLoopAnimation = "anim_knowledgegraph_answer_01"
		TTSGetinAnimation = "anim_knowledgegraph_searching_getout_01"
	} else {
		TTSLoopAnimation = "anim_tts_loop_02"
		TTSGetinAnimation = "anim_getin_tts_01"
	}

	var stopTTSLoop bool
	TTSLoopStopped := make(chan bool)
	for range start {
		if isKG {
			kgStopLooping = true
			for range kgReadyToAnswer {
				break
			}
		} else {
			time.Sleep(time.Millisecond * 300)
		}
		robot.Conn.PlayAnimation(
			ctx,
			&vectorpb.PlayAnimationRequest{
				Animation: &vectorpb.Animation{
					Name: TTSGetinAnimation,
				},
				Loops: 1,
			},
		)
		if !vars.APIConfig.Knowledge.CommandsEnable {
			go func() {
				for {
					if stopTTSLoop {
						TTSLoopStopped <- true
						break
					}
					robot.Conn.PlayAnimation(
						ctx,
						&vectorpb.PlayAnimationRequest{
							Animation: &vectorpb.Animation{
								Name: TTSLoopAnimation,
							},
							Loops: 1,
						},
					)
				}
			}()
		}
		var disconnect bool
		numInResp := 0
		for {
			respSlice := fullRespSlice
			if len(respSlice)-1 < numInResp {
				if !isDone {
					logger.Debug("llm", esn, "waiting for more content from LLM...")
					for range speakReady {
						respSlice = fullRespSlice
						break
					}
					// the wake-up may carry no new sentence (stream error);
					// re-check before indexing
					if len(respSlice)-1 < numInResp {
						continue
					}
				} else {
					break
				}
			}
			if interrupted {
				break
			}
			logger.Debug("llm", esn, respSlice[numInResp])
			acts := GetActionsFromString(respSlice[numInResp])
			nChat[len(nChat)-1].Content = fullRespText
			disconnect = PerformActions(nChat, acts, robot, stopStop)
			if disconnect {
				break
			}
			numInResp = numInResp + 1
		}
		if !vars.APIConfig.Knowledge.CommandsEnable {
			stopTTSLoop = true
			for range TTSLoopStopped {
				break
			}
		}
		time.Sleep(time.Millisecond * 100)
		// if isKG {
		// 	robot.Conn.PlayAnimation(
		// 		ctx,
		// 		&vectorpb.PlayAnimationRequest{
		// 			Animation: &vectorpb.Animation{
		// 				Name: "anim_knowledgegraph_success_01",
		// 			},
		// 			Loops: 1,
		// 		},
		// 	)
		// 	time.Sleep(time.Millisecond * 3300)
		// }
		if !interrupted {
			stopStop <- true
			stop <- true
		}
	}
	return "", nil
}

func KGSim(esn string, textToSay string) error {
	ctx := context.Background()
	matched := false
	var robot *vector.Vector
	var guid string
	var target string
	for _, bot := range vars.BotInfo.Robots {
		if esn == bot.Esn {
			guid = bot.GUID
			target = bot.IPAddress + ":443"
			matched = true
			break
		}
	}
	if matched {
		var err error
		robot, err = vector.New(vector.WithSerialNo(esn), vector.WithToken(guid), vector.WithTarget(target))
		if err != nil {
			return err
		}
	}
	controlRequest := &vectorpb.BehaviorControlRequest{
		RequestType: &vectorpb.BehaviorControlRequest_ControlRequest{
			ControlRequest: &vectorpb.ControlRequest{
				Priority: vectorpb.ControlRequest_OVERRIDE_BEHAVIORS,
			},
		},
	}
	go func() {
		start := make(chan bool)
		stop := make(chan bool)

		go func() {
			// * begin - modified from official vector-go-sdk
			r, err := robot.Conn.BehaviorControl(
				ctx,
			)
			if err != nil {
				log.Println(err)
				return
			}

			if err := r.Send(controlRequest); err != nil {
				log.Println(err)
				return
			}

			for {
				ctrlresp, err := r.Recv()
				if err != nil {
					log.Println(err)
					return
				}
				if ctrlresp.GetControlGrantedResponse() != nil {
					start <- true
					break
				}
			}

			for {
				select {
				case <-stop:
					logger.Debug("llm", esn, "KGSim: releasing behavior control (interrupt)")
					if err := r.Send(
						&vectorpb.BehaviorControlRequest{
							RequestType: &vectorpb.BehaviorControlRequest_ControlRelease{
								ControlRelease: &vectorpb.ControlRelease{},
							},
						},
					); err != nil {
						log.Println(err)
						return
					}
					return
				default:
					continue
				}
			}
			// * end - modified from official vector-go-sdk
		}()

		var stopTTSLoop bool
		var TTSLoopStopped bool
		for range start {
			time.Sleep(time.Millisecond * 300)
			robot.Conn.PlayAnimation(
				ctx,
				&vectorpb.PlayAnimationRequest{
					Animation: &vectorpb.Animation{
						Name: "anim_getin_tts_01",
					},
					Loops: 1,
				},
			)
			go func() {
				for {
					if stopTTSLoop {
						TTSLoopStopped = true
						break
					}
					robot.Conn.PlayAnimation(
						ctx,
						&vectorpb.PlayAnimationRequest{
							Animation: &vectorpb.Animation{
								Name: "anim_tts_loop_02",
							},
							Loops: 1,
						},
					)
				}
			}()
			textToSaySplit := strings.Split(textToSay, ". ")
			for _, str := range textToSaySplit {
				_, err := robot.Conn.SayText(
					ctx,
					&vectorpb.SayTextRequest{
						Text:           str + ".",
						UseVectorVoice: true,
						DurationScalar: 1.0,
					},
				)
				if err != nil {
					logger.Error("llm", esn, "KG SayText error: "+err.Error())
					stop <- true
					break
				}
			}
			stopTTSLoop = true
			for {
				if TTSLoopStopped {
					break
				} else {
					time.Sleep(time.Millisecond * 10)
				}
			}
			time.Sleep(time.Millisecond * 100)
			//time.Sleep(time.Millisecond * 3300)
			stop <- true
		}
	}()
	return nil
}

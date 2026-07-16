package logger

import (
	"fmt"
	"os"
	"regexp"
	"sync"
	"time"
)

type Level int

const (
	DEBUG Level = iota
	INFO
	WARN
	ERROR
)

func (l Level) String() string {
	switch l {
	case INFO:
		return "INFO"
	case WARN:
		return "WARN"
	case ERROR:
		return "ERROR"
	default:
		return "DEBUG"
	}
}

const (
	CompJdocs  = "jdocs"
	CompToken  = "token"
	CompSTT    = "stt"
	CompIntent = "intent"
	CompLLM    = "llm"
	CompSDK    = "sdkapp"
	CompWeb    = "web"
	CompMDNS   = "mdns"
	CompBLE    = "ble"
	CompLua    = "lua"
	CompVoice  = "voice"
	CompConn   = "conn"
)

type Entry struct {
	TimeMS int64  `json:"t"`
	Level  string `json:"level"`
	Comp   string `json:"comp"`
	Bot    string `json:"bot"`
	Msg    string `json:"msg"`
	t      time.Time
}

var (
	LogList  string
	LogArray []string

	LogTrayList  string
	LogTrayArray []string
	LogTrayChan  chan string
)

var (
	initOnce     sync.Once
	debugLogging bool
	logFile      *os.File
	fileFailed   bool

	ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*[A-Za-z]")

	mu        sync.Mutex
	ring      [500]Entry
	ringIdx   int
	ringCount int
)

func doInit() {
	LogTrayChan = make(chan string, 64)
	debugLogging = os.Getenv("DEBUG_LOGGING") == "true"
	if path := os.Getenv("LOG_FILE"); path != "" {
		f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0644)
		if err != nil {
			fileFailed = true
		} else {
			logFile = f
		}
	}
}

func Init() {
	initOnce.Do(doInit)
}

func GetLogTrayChan() chan string {
	initOnce.Do(doInit)
	return LogTrayChan
}

func legacyLine(now time.Time, comp string, bot string, msg string) string {
	s := now.Format("2006.01.02 15:04:05") + ": "
	if comp != "" {
		s = s + "[" + comp + "] "
	}
	if bot != "" {
		s = s + bot + ": "
	}
	return s + msg + "\n"
}

func fileFormatLine(now time.Time, level Level, comp string, bot string, msg string) string {
	s := now.Format("2006.01.02 15:04:05") + " " + level.String() + " "
	if comp != "" {
		s = s + "[" + comp + "] "
	}
	if bot != "" {
		s = s + bot + ": "
	}
	return s + msg + "\n"
}

func parseLevel(s string) Level {
	switch s {
	case "INFO":
		return INFO
	case "WARN":
		return WARN
	case "ERROR":
		return ERROR
	default:
		return DEBUG
	}
}

func logf(level Level, comp string, bot string, msg string, raw string) {
	now := time.Now()
	clean := ansiRe.ReplaceAllString(msg, "")
	e := Entry{
		TimeMS: now.UnixMilli(),
		Level:  level.String(),
		Comp:   comp,
		Bot:    bot,
		Msg:    clean,
		t:      now,
	}
	legacy := legacyLine(now, comp, bot, clean)

	mu.Lock()
	ring[ringIdx] = e
	ringIdx = (ringIdx + 1) % len(ring)
	if ringCount < len(ring) {
		ringCount++
	}

	LogTrayArray = append(LogTrayArray, legacy)
	if len(LogTrayArray) >= 200 {
		LogTrayArray = LogTrayArray[1:]
	}
	LogTrayList = ""
	for _, b := range LogTrayArray {
		LogTrayList = LogTrayList + b
	}

	if level >= INFO {
		LogArray = append(LogArray, legacy)
		if len(LogArray) >= 50 {
			LogArray = LogArray[1:]
		}
		LogList = ""
		for _, b := range LogArray {
			LogList = LogList + b
		}
	}

	if logFile != nil && !fileFailed {
		logFile.WriteString(fileFormatLine(now, level, comp, bot, clean))
	}
	mu.Unlock()

	select {
	case LogTrayChan <- legacy:
	default:
	}
	if debugLogging {
		fmt.Println(raw)
	}
}

func Log(level Level, comp string, bot string, msg string) {
	initOnce.Do(doInit)
	logf(level, comp, bot, msg, msg)
}

func Debug(comp string, bot string, msg string) {
	initOnce.Do(doInit)
	logf(DEBUG, comp, bot, msg, msg)
}

func Info(comp string, bot string, msg string) {
	initOnce.Do(doInit)
	logf(INFO, comp, bot, msg, msg)
}

func Warn(comp string, bot string, msg string) {
	initOnce.Do(doInit)
	logf(WARN, comp, bot, msg, msg)
}

func Error(comp string, bot string, msg string) {
	initOnce.Do(doInit)
	logf(ERROR, comp, bot, msg, msg)
}

func GetEntries(min Level, since int64) []Entry {
	initOnce.Do(doInit)
	mu.Lock()
	defer mu.Unlock()
	out := make([]Entry, 0, ringCount)
	start := 0
	if ringCount == len(ring) {
		start = ringIdx
	}
	for i := 0; i < ringCount; i++ {
		e := ring[(start+i)%len(ring)]
		if parseLevel(e.Level) >= min && e.TimeMS > since {
			out = append(out, e)
		}
	}
	return out
}

func Println(a ...any) {
	initOnce.Do(doInit)
	s := fmt.Sprint(a...)
	logf(DEBUG, "", "", s, s)
}

func LogUI(a ...any) {
	initOnce.Do(doInit)
	s := fmt.Sprint(a...)
	logf(INFO, "", "", s, s)
}

func LogTray(a ...any) {
	Println(a...)
}

package logjson

import (
	"encoding/json"
	"log"
	"time"
)

func Emit(level string, msg string, fields map[string]any) {
	payload := map[string]any{
		"ts":    time.Now().UTC().Format(time.RFC3339Nano),
		"level": level,
		"msg":   msg,
	}
	for k, v := range fields {
		payload[k] = v
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		log.Printf(`{"ts":"%s","level":"error","msg":"logjson marshal failed","error":%q}`, time.Now().UTC().Format(time.RFC3339Nano), err.Error())
		return
	}
	log.Print(string(raw))
}

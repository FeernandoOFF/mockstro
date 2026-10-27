package fixtures

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/feernandooff/mockstro/internal/ojson"
)

func (s *Store) validateMessage(stub *ojson.Object, name, file string) error {
	if stub.Has("request") || stub.Has("response") || stub.Has("extends") {
		return fmt.Errorf("message fixture cannot contain request, response, or extends in %s", file)
	}
	if stub.String("name") != name {
		return fmt.Errorf("stub name must be '%s' in %s", name, file)
	}
	trigger := stub.Object("trigger")
	if trigger == nil {
		return fmt.Errorf("missing trigger object in %s", file)
	}
	switch trigger.String("type") {
	case "message":
		if channel := trigger.Object("channel"); channel != nil {
			if channel.String("type") != "websocket" {
				return fmt.Errorf("message channel must be websocket in %s", file)
			}
			if channel.Object("initiatingRequestPattern") == nil {
				return fmt.Errorf("missing channel.initiatingRequestPattern in %s", file)
			}
		}
	case "http-request":
		if trigger.Object("requestPattern") == nil {
			return fmt.Errorf("missing trigger.requestPattern in %s", file)
		}
	case "http-stub":
		if trigger.String("stubId") == "" {
			return fmt.Errorf("missing trigger.stubId in %s", file)
		}
	default:
		return fmt.Errorf("invalid message trigger type in %s", file)
	}
	value, _ := stub.Get("actions")
	actions, ok := value.([]any)
	if !ok || len(actions) == 0 {
		return fmt.Errorf("message actions must be a non-empty array in %s", file)
	}
	for _, value := range actions {
		action, ok := value.(*ojson.Object)
		if !ok || action.String("type") == "" {
			return fmt.Errorf("invalid message action in %s", file)
		}
		if action.String("type") != "send" {
			return fmt.Errorf("unsupported message action %q in %s", action.String("type"), file)
		}
		message := action.Object("message")
		if message == nil || message.Object("body") == nil {
			return fmt.Errorf("send action requires message.body in %s", file)
		}
		body := message.Object("body")
		if !body.Has("data") && body.String("filePath") == "" {
			return fmt.Errorf("message body requires data or filePath in %s", file)
		}
		if path := body.String("filePath"); path != "" {
			clean := filepath.Clean(path)
			if filepath.IsAbs(path) || clean == ".." || strings.HasPrefix(clean, ".."+string(filepath.Separator)) {
				return fmt.Errorf("unsafe message body file in %s: %s", file, path)
			}
			st, err := os.Stat(filepath.Join(s.P.BodiesRoot(), clean))
			if err != nil || !st.Mode().IsRegular() {
				return fmt.Errorf("missing message body file in %s: %s", file, path)
			}
		}
		if target := action.Object("channelTarget"); target == nil || target.String("type") == "" {
			return fmt.Errorf("send action requires channelTarget in %s", file)
		}
	}
	return nil
}

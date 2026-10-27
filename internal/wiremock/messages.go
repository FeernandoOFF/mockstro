package wiremock

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"sort"
	"time"
)

// closeChannels ends the previous scenario's sessions without restarting the JVM.
// Replace checks ownership before calling this; ordinary HTTP keep-alives are unaffected.
func (s *Server) closeChannels() error {
	data, err := s.do(http.MethodGet, "/__admin/channels", nil)
	if err != nil {
		return fmt.Errorf("list message channels: %w", err)
	}
	var result struct {
		Channels []struct{ ID string } `json:"channels"`
	}
	if err := json.Unmarshal(data, &result); err != nil {
		return fmt.Errorf("read message channels: %w", err)
	}
	for _, channel := range result.Channels {
		if channel.ID == "" {
			return fmt.Errorf("message channel has no id")
		}
		if _, err := s.do(http.MethodDelete, "/__admin/channels/"+url.PathEscape(channel.ID), nil); err != nil {
			return fmt.Errorf("close message channel: %w", err)
		}
	}
	return nil
}

type LoggedMessage struct {
	EventType  string    `json:"eventType"`
	Timestamp  time.Time `json:"timestamp"`
	WasMatched bool      `json:"wasMatched"`
	Channel    struct {
		InitiatingRequest struct {
			URL string `json:"url"`
		} `json:"initiatingRequest"`
	} `json:"channel"`
	Message struct {
		Body string `json:"body"`
	} `json:"message"`
	StubMapping *struct {
		Name string `json:"name"`
	} `json:"stubMapping"`
}

func (m LoggedMessage) StubName() string {
	if m.StubMapping == nil {
		return ""
	}
	return m.StubMapping.Name
}

func (s *Server) MessageJournal(own bool) ([]LoggedMessage, error) {
	if own {
		if err := s.AssertOwn(); err != nil {
			return nil, err
		}
	}
	b, err := s.do(http.MethodGet, "/__admin/messages", nil)
	if err != nil {
		return nil, err
	}
	var journal struct {
		Events []LoggedMessage `json:"messageServeEvents"`
	}
	if err := json.Unmarshal(b, &journal); err != nil {
		return nil, err
	}
	return journal.Events, nil
}

// Traffic includes HTTP calls and both directions of WebSocket messages for the TUI.
func (s *Server) Traffic(own bool) ([]LoggedRequest, error) {
	requests, err := s.Journal(own)
	if err != nil {
		return nil, err
	}
	messages, err := s.MessageJournal(own)
	if err != nil {
		return nil, err
	}
	for _, m := range messages {
		var r LoggedRequest
		r.Request.URL = m.Channel.InitiatingRequest.URL
		r.Request.Body = m.Message.Body
		r.Request.LoggedDate = m.Timestamp.UnixMilli()
		r.Request.Method = "WS in"
		if m.EventType == "sent" {
			r.Request.Method = "WS out"
		}
		r.StubMapping = m.StubMapping
		r.MessageUnmatched = m.EventType == "received" && !m.WasMatched
		requests = append(requests, r)
	}
	sort.SliceStable(requests, func(i, j int) bool { return requests[i].Request.LoggedDate > requests[j].Request.LoggedDate })
	return requests, nil
}

func (s *Server) SendMessage(path, body string) error {
	if err := s.AssertOwn(); err != nil {
		return err
	}
	return s.post("/__admin/channels/send", map[string]any{"type": "websocket", "initiatingRequest": map[string]any{"url": path}, "message": map[string]any{"body": map[string]any{"data": body}}})
}

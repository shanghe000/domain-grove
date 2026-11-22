package action

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

type canvasEvent struct {
	name string
	data []byte
}

type subscriber struct {
	owner  string
	stream chan canvasEvent
}

type eventHub struct {
	mu          sync.Mutex
	subscribers map[*subscriber]bool
}

func (h *eventHub) subscribe(owner string) (*subscriber, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	if len(h.subscribers) >= 512 {
		return nil, false
	}
	if h.subscribers == nil {
		h.subscribers = map[*subscriber]bool{}
	}
	sub := &subscriber{owner: owner, stream: make(chan canvasEvent, 32)}
	h.subscribers[sub] = true
	return sub, true
}

func (h *eventHub) unsubscribe(sub *subscriber) {
	h.mu.Lock()
	defer h.mu.Unlock()
	delete(h.subscribers, sub)
}

func (h *eventHub) publish(name, owner string, payload map[string]any) {
	payload["event"] = name
	body, err := json.Marshal(payload)
	if err != nil {
		return
	}
	event := canvasEvent{name, body}
	h.mu.Lock()
	defer h.mu.Unlock()
	for sub := range h.subscribers {
		if owner != "" && sub.owner != owner {
			continue
		}
		select {
		case sub.stream <- event:
		default:
			close(sub.stream)
			delete(h.subscribers, sub)
		}
	}
}

func (h *eventHub) draft(d *draft) {
	h.publish("draft_updated", d.Payload.Owner, map[string]any{
		"session_id": d.ID, "focus_domain_id": d.Focus, "draft_patch": d.Patch, "draft": d,
	})
}

func (s *Service) stream(w http.ResponseWriter, r *http.Request) error {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return problem(http.StatusServiceUnavailable, "当前连接不支持图谱实时更新。")
	}
	sub, ok := s.events.subscribe(user(r).UserID)
	if !ok {
		return problem(http.StatusServiceUnavailable, "实时连接数量已满，请刷新重试。")
	}
	defer s.events.unsubscribe(sub)
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache, no-transform")
	w.Header().Set("X-Accel-Buffering", "no")
	_, _ = fmt.Fprint(w, ": ready\n\n")
	flusher.Flush()
	heartbeat := time.NewTicker(15 * time.Second)
	defer heartbeat.Stop()
	// Reconnect periodically so the authentication middleware rechecks the live role.
	lifetime := time.NewTimer(55 * time.Second)
	defer lifetime.Stop()
	for {
		select {
		case <-r.Context().Done():
			return nil
		case <-lifetime.C:
			return nil
		case event, open := <-sub.stream:
			if !open {
				return nil
			}
			if _, err := fmt.Fprintf(w, "event: %s\ndata: %s\n\n", event.name, event.data); err != nil {
				return nil
			}
			flusher.Flush()
		case <-heartbeat.C:
			if _, err := fmt.Fprint(w, ": keep-alive\n\n"); err != nil {
				return nil
			}
			flusher.Flush()
		}
	}
}

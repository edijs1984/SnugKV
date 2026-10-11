package webhook

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
	"time"
)

const maxWebhooks = 200

type view struct {
	Definition
	Stats    Stats  `json:"stats"`
	Running  bool   `json:"running"`
	Error    string `json:"error,omitempty"`
	HookURL  string `json:"hookUrl,omitempty"`
	GroupKey string `json:"group,omitempty"`
}

func (m *Manager) view(d Definition) view {
	run, e := m.status(d)
	v := view{Definition: d.redacted(), Stats: m.store.stats(d.ID), Running: run && d.Enabled, Error: e}
	if d.Kind == KindInbound {
		v.HookURL = strings.TrimRight(m.opts.PublicURL, "/") + "/hook/" + d.ID
	} else {
		v.GroupKey = d.group()
	}
	return v
}

func (m *Manager) apiHandler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/status", m.apiStatus)
	mux.HandleFunc("GET /v1/webhooks", m.apiList)
	mux.HandleFunc("POST /v1/webhooks", m.apiCreate)
	mux.HandleFunc("GET /v1/webhooks/{id}", m.apiGet)
	mux.HandleFunc("PUT /v1/webhooks/{id}", m.apiUpdate)
	mux.HandleFunc("DELETE /v1/webhooks/{id}", m.apiDelete)
	mux.HandleFunc("POST /v1/webhooks/{id}/test", m.apiTest)
	mux.HandleFunc("GET /v1/webhooks/{id}/log", m.apiLog)
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if m.opts.APIToken != "" {
			got := strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")
			if subtle.ConstantTimeCompare([]byte(got), []byte(m.opts.APIToken)) != 1 {
				httpJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
		}
		r.Body = http.MaxBytesReader(w, r.Body, 256<<10)
		mux.ServeHTTP(w, r)
	})
}

func apiError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, errNotFound):
		httpJSON(w, http.StatusNotFound, map[string]string{"error": err.Error()})
	default:
		httpJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
}

func badRequest(w http.ResponseWriter, err error) {
	httpJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func (m *Manager) apiStatus(w http.ResponseWriter, r *http.Request) {
	httpJSON(w, http.StatusOK, map[string]any{
		"primary":      m.isPrimary(),
		"secretsReady": m.sealer != nil,
		"inboundReady": m.opts.PublicListen != "",
		"publicUrl":    m.opts.PublicURL,
		"allowPrivate": m.opts.AllowPrivate,
		"maxWebhooks":  maxWebhooks,
	})
}

func (m *Manager) apiList(w http.ResponseWriter, r *http.Request) {
	defs, err := m.store.list()
	if err != nil {
		apiError(w, err)
		return
	}
	out := make([]view, 0, len(defs))
	for _, d := range defs {
		out = append(out, m.view(d))
	}
	httpJSON(w, http.StatusOK, map[string]any{"webhooks": out})
}

func (m *Manager) apiGet(w http.ResponseWriter, r *http.Request) {
	d, err := m.store.get(r.PathValue("id"))
	if err != nil {
		apiError(w, err)
		return
	}
	httpJSON(w, http.StatusOK, m.view(*d))
}

func decodeDef(r *http.Request) (Definition, error) {
	var d Definition
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(&d); err != nil {
		return d, errors.New("invalid JSON: " + err.Error())
	}
	return d, nil
}

func (m *Manager) apiCreate(w http.ResponseWriter, r *http.Request) {
	d, err := decodeDef(r)
	if err != nil {
		badRequest(w, err)
		return
	}
	if d.Kind == KindInbound && m.opts.PublicListen == "" {
		badRequest(w, errors.New("inbound webhooks need -webhooks-listen"))
		return
	}
	existing, err := m.store.list()
	if err != nil {
		apiError(w, err)
		return
	}
	if len(existing) >= maxWebhooks {
		badRequest(w, errors.New("too many webhooks"))
		return
	}
	d.ID = newID()
	d.CreatedMS = time.Now().UnixMilli()
	d.UpdatedMS = d.CreatedMS
	if err := d.normalize(); err != nil {
		badRequest(w, err)
		return
	}
	if err := d.sealSecrets(m.sealer, nil); err != nil {
		badRequest(w, err)
		return
	}
	if err := m.store.put(&d); err != nil {
		apiError(w, err)
		return
	}
	m.reconcile()
	httpJSON(w, http.StatusCreated, m.view(d))
}

func (m *Manager) apiUpdate(w http.ResponseWriter, r *http.Request) {
	prev, err := m.store.get(r.PathValue("id"))
	if err != nil {
		apiError(w, err)
		return
	}
	d, err := decodeDef(r)
	if err != nil {
		badRequest(w, err)
		return
	}
	if d.Kind != "" && d.Kind != prev.Kind {
		badRequest(w, errors.New("the kind of a webhook cannot change"))
		return
	}
	d.ID, d.Kind, d.CreatedMS, d.UpdatedMS = prev.ID, prev.Kind, prev.CreatedMS, time.Now().UnixMilli()
	if err := d.normalize(); err != nil {
		badRequest(w, err)
		return
	}
	if err := d.sealSecrets(m.sealer, prev); err != nil {
		badRequest(w, err)
		return
	}
	if err := m.store.put(&d); err != nil {
		apiError(w, err)
		return
	}
	if prev.Kind == KindOutbound && (prev.Topic != d.Topic || prev.group() != d.group()) {
		m.dropGroup(*prev)
	}
	m.reconcile()
	httpJSON(w, http.StatusOK, m.view(d))
}

// dropGroup removes the consumer group of an outbound webhook that no longer uses it.
func (m *Manager) dropGroup(d Definition) {
	if d.Kind != KindOutbound {
		return
	}
	// The worker may still hold the group; stop it first.
	m.mu.Lock()
	if wk := m.workers[d.ID]; wk != nil {
		wk.cancel()
		delete(m.workers, d.ID)
		m.mu.Unlock()
		<-wk.done
	} else {
		m.mu.Unlock()
	}
	m.kv.do("XGROUP", "DESTROY", d.Topic, d.group())
}

func (m *Manager) apiDelete(w http.ResponseWriter, r *http.Request) {
	d, err := m.store.get(r.PathValue("id"))
	if err != nil {
		apiError(w, err)
		return
	}
	if err := m.store.remove(d.ID); err != nil {
		apiError(w, err)
		return
	}
	m.dropGroup(*d)
	m.reconcile()
	httpJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (m *Manager) apiTest(w http.ResponseWriter, r *http.Request) {
	d, err := m.store.get(r.PathValue("id"))
	if err != nil {
		apiError(w, err)
		return
	}
	if d.Kind != KindOutbound {
		badRequest(w, errors.New("only outbound webhooks can be tested"))
		return
	}
	ctx, cancel := context.WithTimeout(r.Context(), 70*time.Second)
	defer cancel()
	res, err := m.sendTest(ctx, *d)
	if err != nil {
		badRequest(w, err)
		return
	}
	entry := LogEntry{Direction: "out", Status: res.Status, Attempt: 1, Millis: res.Millis, Error: res.Error, MessageID: "test"}
	entry.Result = "ok"
	if !res.OK {
		entry.Result = "failed"
	}
	m.store.logOnly(d.ID, entry)
	httpJSON(w, http.StatusOK, res)
}

func (m *Manager) apiLog(w http.ResponseWriter, r *http.Request) {
	d, err := m.store.get(r.PathValue("id"))
	if err != nil {
		apiError(w, err)
		return
	}
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := m.store.readLog(d.ID, limit)
	if err != nil {
		apiError(w, err)
		return
	}
	httpJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

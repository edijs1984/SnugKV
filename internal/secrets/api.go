package secrets

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"regexp"
	"strconv"
	"strings"
)

type caller struct {
	admin bool
	token *Token
	actor string
}

type ctxKey struct{}

func who(r *http.Request) caller { return r.Context().Value(ctxKey{}).(caller) }

var actorRe = regexp.MustCompile(`^[A-Za-z0-9_.@-]{1,40}$`)

func (m *Manager) handler() http.Handler {
	mux := http.NewServeMux()
	admin := func(pattern string, h http.HandlerFunc) {
		mux.HandleFunc(pattern, func(w http.ResponseWriter, r *http.Request) {
			if !who(r).admin {
				httpJSON(w, http.StatusForbidden, map[string]string{"error": "this needs the admin token"})
				return
			}
			h(w, r)
		})
	}
	reader := func(pattern string, h http.HandlerFunc) { mux.HandleFunc(pattern, h) }

	reader("GET /v1/status", m.apiStatus)
	admin("GET /v1/envs", m.apiEnvs)
	admin("PUT /v1/envs/{project}/{env}", m.apiCreateEnv)
	admin("DELETE /v1/envs/{project}/{env}", m.apiDeleteEnv)
	admin("GET /v1/envs/{project}/{env}/secrets", m.apiListSecrets)
	reader("GET /v1/envs/{project}/{env}/secrets/{name}", m.apiGetSecret)
	admin("PUT /v1/envs/{project}/{env}/secrets/{name}", m.apiSetSecret)
	admin("DELETE /v1/envs/{project}/{env}/secrets/{name}", m.apiDeleteSecret)
	admin("GET /v1/envs/{project}/{env}/secrets/{name}/versions", m.apiVersions)
	admin("POST /v1/envs/{project}/{env}/secrets/{name}/rollback", m.apiRollback)
	admin("POST /v1/envs/{project}/{env}/import", m.apiImport)
	reader("GET /v1/envs/{project}/{env}/export", m.apiExport)
	admin("GET /v1/audit", m.apiAudit)
	admin("GET /v1/tokens", m.apiTokens)
	admin("POST /v1/tokens", m.apiCreateToken)
	admin("DELETE /v1/tokens/{id}", m.apiDeleteToken)

	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Cache-Control", "no-store")
		// The API is for services and tools, never for a browser page: a web
		// page could otherwise send it requests (CSRF), or reach a token-less
		// loopback API through DNS rebinding.
		if r.Header.Get("Origin") != "" {
			httpJSON(w, http.StatusForbidden, map[string]string{"error": "browser requests are not accepted"})
			return
		}
		if m.opts.APIToken == "" && !loopbackHost(r.Host) {
			httpJSON(w, http.StatusForbidden, map[string]string{"error": "without -secrets-api-token the API answers only on localhost"})
			return
		}
		got, _ := strings.CutPrefix(r.Header.Get("Authorization"), "Bearer ")
		var c caller
		switch {
		case strings.HasPrefix(got, "sks_") || strings.HasPrefix(got, "ska_"):
			t, ok := m.tokenFor(got)
			if !ok {
				httpJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
				return
			}
			c = caller{token: t, actor: "token:" + t.Name}
			if t.Admin {
				c = caller{admin: true, token: t, actor: "admin:" + t.Name}
				if a := r.Header.Get("X-Snugkv-Actor"); actorRe.MatchString(a) {
					c.actor += "/" + a
				}
			}
		case m.opts.APIToken == "" || subtle.ConstantTimeCompare([]byte(got), []byte(m.opts.APIToken)) == 1:
			c = caller{admin: true, actor: "admin"}
			// Tools in front of the API (the console) can name who acts
			if a := r.Header.Get("X-Snugkv-Actor"); actorRe.MatchString(a) {
				c.actor = "admin:" + a
			}
		default:
			httpJSON(w, http.StatusUnauthorized, map[string]string{"error": "unauthorized"})
			return
		}
		r.Body = http.MaxBytesReader(w, r.Body, 1<<20)
		mux.ServeHTTP(w, r.WithContext(context.WithValue(r.Context(), ctxKey{}, c)))
	})
}

func httpJSON(w http.ResponseWriter, status int, body any) {
	b, _ := json.Marshal(body)
	w.Header().Set("Content-Type", "application/json")
	w.Header().Set("Content-Length", strconv.Itoa(len(b)))
	w.WriteHeader(status)
	w.Write(b)
}

func badRequest(w http.ResponseWriter, err error) {
	httpJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
}

func apiError(w http.ResponseWriter, err error) {
	var se serverError
	switch {
	case errors.Is(err, errNotFound):
		httpJSON(w, http.StatusNotFound, map[string]string{"error": "not found"})
	case errors.As(err, &se) && strings.HasPrefix(string(se), "READONLY"):
		httpJSON(w, http.StatusConflict, map[string]string{"error": "this server is a replica; change secrets on the primary"})
	default:
		httpJSON(w, http.StatusBadGateway, map[string]string{"error": err.Error()})
	}
}

func decode(r *http.Request, v any) error {
	dec := json.NewDecoder(r.Body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return errors.New("invalid JSON: " + err.Error())
	}
	return nil
}

// env reads and checks {project}/{env}; a read token must cover it.
func (m *Manager) env(w http.ResponseWriter, r *http.Request, mustExist bool) (string, bool) {
	env, err := envName(r.PathValue("project"), r.PathValue("env"))
	if err != nil {
		badRequest(w, err)
		return "", false
	}
	if c := who(r); !c.admin && !c.token.allows(env) {
		httpJSON(w, http.StatusForbidden, map[string]string{"error": "this token cannot read " + env})
		return "", false
	}
	if mustExist {
		ok, err := m.envExists(env)
		if err != nil {
			apiError(w, err)
			return "", false
		}
		if !ok {
			httpJSON(w, http.StatusNotFound, map[string]string{"error": "environment " + env + " not found"})
			return "", false
		}
	}
	return env, true
}

func (m *Manager) secretName(w http.ResponseWriter, r *http.Request) (string, bool) {
	name := r.PathValue("name")
	if err := checkName(name); err != nil {
		badRequest(w, err)
		return "", false
	}
	return name, true
}

func (m *Manager) apiStatus(w http.ResponseWriter, r *http.Request) {
	out := map[string]any{"ready": true, "primary": m.isPrimary()}
	if c := who(r); !c.admin {
		out["token"] = map[string]any{"name": c.token.Name, "envs": c.token.Envs, "expiresMs": c.token.ExpiresMs}
	} else {
		out["maxEnvs"], out["maxSecrets"], out["historyKept"] = maxEnvs, maxSecrets, histKeep
	}
	httpJSON(w, http.StatusOK, out)
}

func (m *Manager) isPrimary() bool {
	v, err := m.kv.do("INFO", "replication")
	if err != nil {
		return true
	}
	for _, line := range strings.Split(v.str, "\n") {
		if role, ok := strings.CutPrefix(strings.TrimSpace(line), "role:"); ok {
			return role == "master"
		}
	}
	return true
}

func (m *Manager) apiEnvs(w http.ResponseWriter, r *http.Request) {
	envs, err := m.listEnvs()
	if err != nil {
		apiError(w, err)
		return
	}
	httpJSON(w, http.StatusOK, map[string]any{"envs": envs})
}

func (m *Manager) apiCreateEnv(w http.ResponseWriter, r *http.Request) {
	env, ok := m.env(w, r, false)
	if !ok {
		return
	}
	created, err := m.createEnv(env)
	if err != nil {
		apiError(w, err)
		return
	}
	status := http.StatusOK
	if created {
		m.audit("env.create", env, "", who(r).actor)
		status = http.StatusCreated
	}
	httpJSON(w, status, map[string]any{"name": env, "created": created})
}

func (m *Manager) apiDeleteEnv(w http.ResponseWriter, r *http.Request) {
	env, ok := m.env(w, r, true)
	if !ok {
		return
	}
	if err := m.deleteEnv(env); err != nil {
		apiError(w, err)
		return
	}
	m.audit("env.delete", env, "", who(r).actor)
	httpJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (m *Manager) apiListSecrets(w http.ResponseWriter, r *http.Request) {
	env, ok := m.env(w, r, true)
	if !ok {
		return
	}
	reveal := r.URL.Query().Get("reveal") == "1"
	list, err := m.listSecrets(env, reveal)
	if err != nil {
		apiError(w, err)
		return
	}
	if reveal {
		m.audit("reveal.all", env, "", who(r).actor)
	}
	httpJSON(w, http.StatusOK, map[string]any{"env": env, "secrets": list})
}

func (m *Manager) apiGetSecret(w http.ResponseWriter, r *http.Request) {
	env, ok := m.env(w, r, true)
	if !ok {
		return
	}
	name, ok := m.secretName(w, r)
	if !ok {
		return
	}
	plain, rec, err := m.getSecret(env, name)
	if err != nil {
		apiError(w, err)
		return
	}
	m.audit("read", env, name, who(r).actor)
	httpJSON(w, http.StatusOK, SecretInfo{Name: name, Hint: hint(plain), Version: rec.Version, UpdatedMs: rec.Ms, UpdatedBy: rec.By, Value: plain})
}

func (m *Manager) apiSetSecret(w http.ResponseWriter, r *http.Request) {
	env, ok := m.env(w, r, false)
	if !ok {
		return
	}
	name, ok := m.secretName(w, r)
	if !ok {
		return
	}
	var body struct {
		Value *string `json:"value"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	if body.Value == nil {
		badRequest(w, errors.New(`"value" is required`))
		return
	}
	if _, err := m.createEnv(env); err != nil {
		apiError(w, err)
		return
	}
	ver, err := m.setSecret(env, name, *body.Value, who(r).actor)
	if err != nil {
		if strings.HasPrefix(err.Error(), "a secret value") || strings.HasPrefix(err.Error(), "too many") {
			badRequest(w, err)
		} else {
			apiError(w, err)
		}
		return
	}
	changed := ver != 0
	if changed {
		m.audit("set", env, name, who(r).actor)
	}
	httpJSON(w, http.StatusOK, map[string]any{"name": name, "version": ver, "changed": changed})
}

func (m *Manager) apiDeleteSecret(w http.ResponseWriter, r *http.Request) {
	env, ok := m.env(w, r, true)
	if !ok {
		return
	}
	name, ok := m.secretName(w, r)
	if !ok {
		return
	}
	if err := m.deleteSecret(env, name, who(r).actor); err != nil {
		apiError(w, err)
		return
	}
	m.audit("delete", env, name, who(r).actor)
	httpJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func (m *Manager) apiVersions(w http.ResponseWriter, r *http.Request) {
	env, ok := m.env(w, r, true)
	if !ok {
		return
	}
	name, ok := m.secretName(w, r)
	if !ok {
		return
	}
	vs, err := m.versions(env, name)
	if err != nil {
		apiError(w, err)
		return
	}
	httpJSON(w, http.StatusOK, map[string]any{"versions": vs})
}

func (m *Manager) apiRollback(w http.ResponseWriter, r *http.Request) {
	env, ok := m.env(w, r, true)
	if !ok {
		return
	}
	name, ok := m.secretName(w, r)
	if !ok {
		return
	}
	var body struct {
		Version int64 `json:"version"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	ver, err := m.rollback(env, name, body.Version, who(r).actor)
	if err != nil {
		badRequest(w, err)
		return
	}
	m.audit("rollback", env, name, who(r).actor)
	httpJSON(w, http.StatusOK, map[string]any{"name": name, "version": ver, "restored": body.Version})
}

func (m *Manager) apiImport(w http.ResponseWriter, r *http.Request) {
	env, ok := m.env(w, r, false)
	if !ok {
		return
	}
	var body struct {
		Dotenv    string            `json:"dotenv"`
		Values    map[string]string `json:"values"`
		Overwrite bool              `json:"overwrite"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	vals := body.Values
	if body.Dotenv != "" {
		parsed, err := ParseDotenv(body.Dotenv)
		if err != nil {
			badRequest(w, err)
			return
		}
		if vals == nil {
			vals = map[string]string{}
		}
		for k, v := range parsed {
			vals[k] = v
		}
	}
	if len(vals) == 0 {
		badRequest(w, errors.New("nothing to import"))
		return
	}
	for k, v := range vals {
		if err := checkName(k); err != nil {
			badRequest(w, errors.New(k+": "+err.Error()))
			return
		}
		if len(v) > maxValueLen {
			badRequest(w, errors.New(k+": value too long"))
			return
		}
	}
	if _, err := m.createEnv(env); err != nil {
		apiError(w, err)
		return
	}
	res := map[string][]string{"created": {}, "updated": {}, "unchanged": {}, "skipped": {}}
	actor := who(r).actor
	for _, k := range sortedKeys(vals) {
		_, err := m.current(env, k)
		exists := err == nil
		if err != nil && !errors.Is(err, errNotFound) {
			apiError(w, err)
			return
		}
		if exists && !body.Overwrite {
			res["skipped"] = append(res["skipped"], k)
			continue
		}
		ver, err := m.setSecret(env, k, vals[k], actor)
		if err != nil {
			apiError(w, err)
			return
		}
		switch {
		case ver == 0:
			res["unchanged"] = append(res["unchanged"], k)
		case exists:
			res["updated"] = append(res["updated"], k)
			m.audit("set", env, k, actor)
		default:
			res["created"] = append(res["created"], k)
			m.audit("set", env, k, actor)
		}
	}
	httpJSON(w, http.StatusOK, res)
}

func (m *Manager) apiExport(w http.ResponseWriter, r *http.Request) {
	env, ok := m.env(w, r, true)
	if !ok {
		return
	}
	vals, err := m.values(env)
	if err != nil {
		apiError(w, err)
		return
	}
	m.audit("export", env, "", who(r).actor)
	if r.URL.Query().Get("format") == "dotenv" {
		w.Header().Set("Content-Type", "text/plain; charset=utf-8")
		w.Write([]byte(FormatDotenv(vals)))
		return
	}
	httpJSON(w, http.StatusOK, map[string]any{"env": env, "values": vals})
}

func (m *Manager) apiAudit(w http.ResponseWriter, r *http.Request) {
	limit, _ := strconv.Atoi(r.URL.Query().Get("limit"))
	entries, err := m.readAudit(limit, r.URL.Query().Get("env"))
	if err != nil {
		apiError(w, err)
		return
	}
	httpJSON(w, http.StatusOK, map[string]any{"entries": entries})
}

func (m *Manager) apiTokens(w http.ResponseWriter, r *http.Request) {
	ts, err := m.listTokens()
	if err != nil {
		apiError(w, err)
		return
	}
	httpJSON(w, http.StatusOK, map[string]any{"tokens": ts})
}

func (m *Manager) apiCreateToken(w http.ResponseWriter, r *http.Request) {
	var body struct {
		Name    string   `json:"name"`
		Envs    []string `json:"envs"`
		Admin   bool     `json:"admin"`
		TTLDays int      `json:"ttlDays"`
	}
	if err := decode(r, &body); err != nil {
		badRequest(w, err)
		return
	}
	t, secret, err := m.createToken(body.Name, body.Envs, body.Admin, body.TTLDays)
	if err != nil {
		badRequest(w, err)
		return
	}
	action := "token.create"
	if t.Admin {
		action = "token.create.admin"
	}
	m.audit(action, "", t.Name, who(r).actor)
	httpJSON(w, http.StatusCreated, map[string]any{"token": t, "secret": secret})
}

func (m *Manager) apiDeleteToken(w http.ResponseWriter, r *http.Request) {
	id := r.PathValue("id")
	if !idRe.MatchString(id) {
		apiError(w, errNotFound)
		return
	}
	if err := m.deleteToken(id); err != nil {
		apiError(w, err)
		return
	}
	m.audit("token.delete", "", id, who(r).actor)
	httpJSON(w, http.StatusOK, map[string]bool{"ok": true})
}

func sortedKeys(m map[string]string) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sortStrings(out)
	return out
}

// loopbackHost reports whether a Host header names this machine.
func loopbackHost(host string) bool {
	if h, _, err := net.SplitHostPort(host); err == nil {
		host = h
	}
	host = strings.Trim(host, "[]")
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

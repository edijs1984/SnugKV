package webhook

import (
	"crypto/rand"
	"encoding/base32"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strings"
)

// Kind of a webhook.
const (
	KindInbound  = "inbound"  // an HTTP endpoint that publishes into a stream
	KindOutbound = "outbound" // a consumer that POSTs stream messages to a URL
)

// SecretField holds a secret. On input Plain carries the new value; at rest
// only the encrypted form and a hint (last four characters) are kept; on
// output only Set and Hint are shown.
type SecretField struct {
	Plain string `json:"value,omitempty"`
	Enc   string `json:"enc,omitempty"`
	Hint  string `json:"hint,omitempty"`
	Set   bool   `json:"set,omitempty"`
}

// Header is one custom outbound header, for example an API key.
type Header struct {
	Name   string `json:"name"`
	Value  string `json:"value,omitempty"` // plain for normal headers; input only for secret ones
	Secret bool   `json:"secret,omitempty"`
	Enc    string `json:"enc,omitempty"`
	Hint   string `json:"hint,omitempty"`
	Set    bool   `json:"set,omitempty"`
}

// InboundAuth says how the sender proves who it is.
type InboundAuth struct {
	Mode   string      `json:"mode"`             // none, token or hmac
	Header string      `json:"header,omitempty"` // header carrying the token or signature
	Algo   string      `json:"algo,omitempty"`   // hmac: sha256 (default) or sha1
	Prefix string      `json:"prefix,omitempty"` // hmac: text before the hex digest, e.g. "sha256="
	Secret SecretField `json:"secret"`
}

// Inbound configures an endpoint at POST /hook/<id>.
type Inbound struct {
	Auth         InboundAuth `json:"auth"`
	Event        string      `json:"event,omitempty"`       // fixed event name
	EventHeader  string      `json:"eventHeader,omitempty"` // or take it from this request header
	MaxPerSecond int         `json:"maxPerSecond,omitempty"`
	MaxLen       int         `json:"maxLen,omitempty"` // trim the stream to about this many entries
}

// Outbound configures a consumer that delivers to a URL.
type Outbound struct {
	URL           string      `json:"url"`
	Method        string      `json:"method,omitempty"`
	Headers       []Header    `json:"headers,omitempty"`
	SigningSecret SecretField `json:"signingSecret"`
	Body          string      `json:"body,omitempty"` // envelope (default) or payload
	Group         string      `json:"group,omitempty"`
	From          string      `json:"from,omitempty"` // "$" new messages only (default) or "0" the whole stream
	MaxAttempts   int         `json:"maxAttempts,omitempty"`
	TimeoutMS     int         `json:"timeoutMs,omitempty"`
	DeadLetter    *bool       `json:"deadLetter,omitempty"` // publish failures to <topic>.dlq (default true)
}

// Definition is a webhook.
type Definition struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	Kind      string    `json:"kind"`
	Topic     string    `json:"topic"` // the stream key
	Enabled   bool      `json:"enabled"`
	CreatedMS int64     `json:"createdMs"`
	UpdatedMS int64     `json:"updatedMs"`
	Inbound   *Inbound  `json:"inbound,omitempty"`
	Outbound  *Outbound `json:"outbound,omitempty"`
}

var (
	headerName = regexp.MustCompile("^[A-Za-z0-9!#$%&'*+\\-.^_`|~]{1,100}$")
	forbidden  = map[string]bool{"host": true, "content-length": true, "transfer-encoding": true, "connection": true, "content-type": false}
	idRe       = regexp.MustCompile(`^[a-z2-7]{16}$`)
)

func newID() string {
	b := make([]byte, 10)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return strings.ToLower(base32.StdEncoding.EncodeToString(b)) // 16 chars, 80 bits
}

func (d *Definition) deadLetter() bool {
	return d.Outbound == nil || d.Outbound.DeadLetter == nil || *d.Outbound.DeadLetter
}

func (d *Definition) group() string {
	if d.Outbound != nil && d.Outbound.Group != "" {
		return d.Outbound.Group
	}
	return "webhook:" + d.ID
}

// normalize fills defaults and checks every field. It does not touch secrets.
func (d *Definition) normalize() error {
	d.Name = strings.TrimSpace(d.Name)
	if d.Name == "" || len(d.Name) > 80 {
		return errors.New("name is required (1-80 characters)")
	}
	d.Topic = strings.TrimSpace(d.Topic)
	if d.Topic == "" || len(d.Topic) > 200 || strings.ContainsAny(d.Topic, " \t\r\n\x00") {
		return errors.New("topic is required (no spaces, up to 200 characters)")
	}
	switch d.Kind {
	case KindInbound:
		if d.Inbound == nil {
			d.Inbound = &Inbound{}
		}
		d.Outbound = nil
		return d.Inbound.normalize()
	case KindOutbound:
		if d.Outbound == nil {
			return errors.New("outbound settings are required")
		}
		d.Inbound = nil
		return d.Outbound.normalize()
	}
	return fmt.Errorf("kind must be %q or %q", KindInbound, KindOutbound)
}

func (in *Inbound) normalize() error {
	a := &in.Auth
	if a.Mode == "" {
		a.Mode = "none"
	}
	switch a.Mode {
	case "none":
		a.Header, a.Algo, a.Prefix = "", "", ""
	case "token":
		if a.Header == "" {
			a.Header = "X-Webhook-Token"
		}
		a.Algo, a.Prefix = "", ""
	case "hmac":
		if a.Header == "" {
			a.Header = "X-Hub-Signature-256"
		}
		if a.Algo == "" {
			a.Algo = "sha256"
		}
		if a.Algo != "sha256" && a.Algo != "sha1" {
			return errors.New("hmac algo must be sha256 or sha1")
		}
	default:
		return errors.New("auth mode must be none, token or hmac")
	}
	if a.Header != "" && !headerName.MatchString(a.Header) {
		return errors.New("auth header is not a valid header name")
	}
	if in.EventHeader != "" && !headerName.MatchString(in.EventHeader) {
		return errors.New("event header is not a valid header name")
	}
	if len(in.Event) > 200 {
		return errors.New("event is too long")
	}
	if in.MaxPerSecond < 0 || in.MaxPerSecond > 100000 {
		return errors.New("maxPerSecond must be between 0 and 100000")
	}
	if in.MaxPerSecond == 0 {
		in.MaxPerSecond = 100
	}
	if in.MaxLen < 0 {
		return errors.New("maxLen cannot be negative")
	}
	return nil
}

func (o *Outbound) normalize() error {
	u, err := url.Parse(strings.TrimSpace(o.URL))
	if err != nil || (u.Scheme != "http" && u.Scheme != "https") {
		return errors.New("url must be an http:// or https:// address")
	}
	if u.Hostname() == "" {
		return errNoHost
	}
	if u.User != nil {
		return errors.New("put credentials in headers, not in the url")
	}
	o.URL = u.String()
	o.Method = strings.ToUpper(o.Method)
	if o.Method == "" {
		o.Method = "POST"
	}
	if o.Method != "POST" && o.Method != "PUT" && o.Method != "PATCH" {
		return errors.New("method must be POST, PUT or PATCH")
	}
	seen := map[string]bool{}
	for i := range o.Headers {
		h := &o.Headers[i]
		h.Name = strings.TrimSpace(h.Name)
		if !headerName.MatchString(h.Name) {
			return fmt.Errorf("header %q is not a valid header name", h.Name)
		}
		l := strings.ToLower(h.Name)
		if forbidden[l] || strings.HasPrefix(l, "x-snugkv-") {
			return fmt.Errorf("header %q is set by SnugKV and cannot be overridden", h.Name)
		}
		if seen[l] {
			return fmt.Errorf("header %q appears twice", h.Name)
		}
		seen[l] = true
		if strings.ContainsAny(h.Value, "\r\n\x00") {
			return fmt.Errorf("header %q has an invalid value", h.Name)
		}
	}
	if len(o.Headers) > 30 {
		return errors.New("at most 30 custom headers")
	}
	switch o.Body {
	case "":
		o.Body = "envelope"
	case "envelope", "payload":
	default:
		return errors.New("body must be envelope or payload")
	}
	switch o.From {
	case "":
		o.From = "$"
	case "$", "0":
	default:
		return errors.New(`from must be "$" (new messages) or "0" (whole stream)`)
	}
	if o.MaxAttempts == 0 {
		o.MaxAttempts = 5
	}
	if o.MaxAttempts < 1 || o.MaxAttempts > 20 {
		return errors.New("maxAttempts must be between 1 and 20")
	}
	if o.TimeoutMS == 0 {
		o.TimeoutMS = 10000
	}
	if o.TimeoutMS < 500 || o.TimeoutMS > 60000 {
		return errors.New("timeoutMs must be between 500 and 60000")
	}
	if o.Group != "" && (len(o.Group) > 200 || strings.ContainsAny(o.Group, " \t\r\n\x00")) {
		return errors.New("group has invalid characters")
	}
	return nil
}

// redacted returns a copy that is safe to show: no plaintext, no ciphertext.
func (d Definition) redacted() Definition {
	if d.Inbound != nil {
		in := *d.Inbound
		in.Auth.Secret = SecretField{Set: in.Auth.Secret.Enc != "", Hint: in.Auth.Secret.Hint}
		d.Inbound = &in
	}
	if d.Outbound != nil {
		o := *d.Outbound
		o.SigningSecret = SecretField{Set: o.SigningSecret.Enc != "", Hint: o.SigningSecret.Hint}
		hs := make([]Header, len(o.Headers))
		for i, h := range o.Headers {
			if h.Secret {
				hs[i] = Header{Name: h.Name, Secret: true, Set: h.Enc != "", Hint: h.Hint}
			} else {
				hs[i] = Header{Name: h.Name, Value: h.Value}
			}
		}
		o.Headers = hs
		d.Outbound = &o
	}
	return d
}

// sealSecrets encrypts plaintext secrets in d. Where a secret is not supplied
// (empty Plain) the value from prev, if any, is kept, so editing a webhook
// never needs the secrets typed again.
func (d *Definition) sealSecrets(s *sealer, prev *Definition) error {
	field := func(f *SecretField, old SecretField) error {
		f.Set = false
		switch {
		case f.Plain != "":
			enc, err := s.seal(f.Plain)
			if err != nil {
				return err
			}
			*f = SecretField{Enc: enc, Hint: hint(f.Plain)}
		default:
			*f = SecretField{Enc: old.Enc, Hint: old.Hint}
		}
		return nil
	}
	if d.Inbound != nil {
		var old SecretField
		if prev != nil && prev.Inbound != nil {
			old = prev.Inbound.Auth.Secret
		}
		if err := field(&d.Inbound.Auth.Secret, old); err != nil {
			return err
		}
		if d.Inbound.Auth.Mode != "none" && d.Inbound.Auth.Secret.Enc == "" {
			return errors.New("auth secret is required for token and hmac")
		}
		if d.Inbound.Auth.Mode == "none" {
			d.Inbound.Auth.Secret = SecretField{}
		}
	}
	if d.Outbound != nil {
		var old SecretField
		var oldHeaders map[string]Header
		if prev != nil && prev.Outbound != nil {
			old = prev.Outbound.SigningSecret
			oldHeaders = map[string]Header{}
			for _, h := range prev.Outbound.Headers {
				oldHeaders[strings.ToLower(h.Name)] = h
			}
		}
		if err := field(&d.Outbound.SigningSecret, old); err != nil {
			return err
		}
		for i := range d.Outbound.Headers {
			h := &d.Outbound.Headers[i]
			h.Set = false
			if !h.Secret {
				h.Enc, h.Hint = "", ""
				continue
			}
			if h.Value != "" {
				enc, err := s.seal(h.Value)
				if err != nil {
					return err
				}
				h.Enc, h.Hint, h.Value = enc, hint(h.Value), ""
			} else if oh, ok := oldHeaders[strings.ToLower(h.Name)]; ok && oh.Secret {
				h.Enc, h.Hint = oh.Enc, oh.Hint
			}
			if h.Enc == "" {
				return fmt.Errorf("header %q needs a value", h.Name)
			}
		}
	}
	return nil
}

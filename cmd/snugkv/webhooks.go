package main

import (
	"flag"
	"log"
	"net"
	"os"
	"strings"

	"snugkv/internal/webhook"
)

// webhookFlags are the options of the optional webhook module. Secrets can be
// given through the environment so they stay out of process listings.
type webhookFlags struct {
	listen, apiListen, apiToken, key, auth, publicURL string
	allowPrivate                                      bool
}

func (w *webhookFlags) register() {
	flag.StringVar(&w.listen, "webhooks-listen", os.Getenv("SNUGKV_WEBHOOKS_LISTEN"), "serve inbound webhooks (POST /hook/<id>) on this address, for example :8090; enables the webhook module (optional)")
	flag.StringVar(&w.apiListen, "webhooks-api-listen", os.Getenv("SNUGKV_WEBHOOKS_API_LISTEN"), "serve the webhook management API on this address, for example 127.0.0.1:8091; enables the webhook module (optional)")
	flag.StringVar(&w.apiToken, "webhooks-api-token", os.Getenv("SNUGKV_WEBHOOKS_API_TOKEN"), "bearer token for the management API (env SNUGKV_WEBHOOKS_API_TOKEN); required unless the API listens on loopback")
	flag.StringVar(&w.key, "webhooks-key", os.Getenv("SNUGKV_WEBHOOKS_KEY"), "key (16+ characters) that encrypts stored webhook secrets (env SNUGKV_WEBHOOKS_KEY)")
	flag.StringVar(&w.auth, "webhooks-auth", os.Getenv("SNUGKV_WEBHOOKS_AUTH"), "credentials the module uses against this server, password or user:password (env SNUGKV_WEBHOOKS_AUTH)")
	flag.StringVar(&w.publicURL, "webhooks-public-url", os.Getenv("SNUGKV_WEBHOOKS_PUBLIC_URL"), "externally visible base URL of -webhooks-listen, shown in the API")
	flag.BoolVar(&w.allowPrivate, "webhooks-allow-private", false, "let outbound webhooks reach private and loopback addresses (blocked by default)")
}

// start launches the module when a listener was requested.
func (w *webhookFlags) start(serverAddr string) (*webhook.Manager, error) {
	if w.listen == "" && w.apiListen == "" {
		return nil, nil
	}
	host, port, err := net.SplitHostPort(serverAddr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	user, pass := "", w.auth
	if i := strings.Index(w.auth, ":"); i > 0 {
		user, pass = w.auth[:i], w.auth[i+1:]
	}
	m, err := webhook.Start(webhook.Options{
		KVAddr: net.JoinHostPort(host, port), KVUser: user, KVPass: pass,
		PublicListen: w.listen, APIListen: w.apiListen, APIToken: w.apiToken,
		Key: w.key, AllowPrivate: w.allowPrivate, PublicURL: w.publicURL,
		Logf: func(format string, args ...any) { log.Printf("event=webhooks "+format, args...) },
	})
	if err != nil {
		return nil, err
	}
	log.Printf("event=webhooks_started listeners=%v", m.Addrs())
	return m, nil
}

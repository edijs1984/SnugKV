package main

import (
	"flag"
	"log"
	"net"
	"os"
	"strings"

	"snugkv/internal/secrets"
)

// secretsFlags are the options of the optional secrets module. The key and
// tokens can come from the environment so they stay out of process listings.
type secretsFlags struct {
	apiListen, apiToken, key, oldKey, auth string
}

func (s *secretsFlags) register() {
	flag.StringVar(&s.apiListen, "secrets-api-listen", os.Getenv("SNUGKV_SECRETS_API_LISTEN"), "serve the secrets API on this address, for example 127.0.0.1:8092; enables the secrets module (optional)")
	flag.StringVar(&s.apiToken, "secrets-api-token", os.Getenv("SNUGKV_SECRETS_API_TOKEN"), "admin bearer token of the secrets API (env SNUGKV_SECRETS_API_TOKEN); required unless the API listens on loopback")
	flag.StringVar(&s.key, "secrets-key", os.Getenv("SNUGKV_SECRETS_KEY"), "key (16+ characters) that encrypts stored secrets (env SNUGKV_SECRETS_KEY); required by the secrets module")
	flag.StringVar(&s.oldKey, "secrets-old-key", os.Getenv("SNUGKV_SECRETS_OLD_KEY"), "previous -secrets-key during a key rotation: values still sealed with it are re-encrypted with the new key at start; remove it afterwards (env SNUGKV_SECRETS_OLD_KEY)")
	flag.StringVar(&s.auth, "secrets-auth", os.Getenv("SNUGKV_SECRETS_AUTH"), "credentials the module uses against this server, password or user:password (env SNUGKV_SECRETS_AUTH)")
}

// start launches the module when its API address was given.
func (s *secretsFlags) start(serverAddr string) (*secrets.Manager, error) {
	if s.apiListen == "" {
		return nil, nil
	}
	host, port, err := net.SplitHostPort(serverAddr)
	if err != nil {
		return nil, err
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	user, pass := "", s.auth
	if i := strings.Index(s.auth, ":"); i > 0 {
		user, pass = s.auth[:i], s.auth[i+1:]
	}
	m, err := secrets.Start(secrets.Options{
		KVAddr: net.JoinHostPort(host, port), KVUser: user, KVPass: pass,
		APIListen: s.apiListen, APIToken: s.apiToken, Key: s.key, OldKey: s.oldKey,
		Logf: func(format string, args ...any) { log.Printf("event=secrets "+format, args...) },
	})
	if err != nil {
		return nil, err
	}
	log.Printf("event=secrets_started api=%s", m.Addr())
	return m, nil
}

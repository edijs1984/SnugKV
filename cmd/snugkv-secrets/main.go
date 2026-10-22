// Command snugkv-secrets reads secrets from the SnugKV secrets module.
//
//	snugkv-secrets run --env airsig/prod -- node dist/main.js
//	snugkv-secrets export --env airsig/prod > .env
//	snugkv-secrets get --env airsig/prod DATABASE_URL
//
// The API address and a read token come from SNUGKV_SECRETS_URL and
// SNUGKV_SECRETS_TOKEN (or --url / --token). `run` starts the command with
// the secrets as environment variables; the token itself is not passed on.
package main

import (
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"regexp"
	"sort"
	"strings"
	"syscall"
	"time"

	"snugkv/internal/secrets"
)

const usage = `snugkv-secrets: read secrets from the SnugKV secrets module

Usage:
  snugkv-secrets run    --env project/env [--override] -- command [args...]
  snugkv-secrets export --env project/env [--format dotenv|json]
  snugkv-secrets get    --env project/env NAME

Options (every command):
  --url u     secrets API, e.g. http://127.0.0.1:8092 (env SNUGKV_SECRETS_URL)
  --token t   read token sks_... or the admin token (env SNUGKV_SECRETS_TOKEN)
  --env e     environment, e.g. airsig/prod (env SNUGKV_SECRETS_ENV)
  --wait d    keep retrying this long while the API is unreachable (default 30s)

run starts the command with the secrets added to its environment. Variables
that are already set win, so a value in docker-compose can override a secret;
--override lets the secrets win instead. SNUGKV_SECRETS_TOKEN is removed from
the command's environment.
`

type options struct {
	url, token, env, format string
	wait                    time.Duration
	override                bool
}

func main() {
	os.Exit(realMain(os.Args[1:], os.Stdout, os.Stderr))
}

func realMain(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" || args[0] == "help" {
		fmt.Fprint(stderr, usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	cmd := args[0]
	fs := flag.NewFlagSet(cmd, flag.ContinueOnError)
	fs.SetOutput(stderr)
	var o options
	fs.StringVar(&o.url, "url", os.Getenv("SNUGKV_SECRETS_URL"), "")
	fs.StringVar(&o.token, "token", os.Getenv("SNUGKV_SECRETS_TOKEN"), "")
	fs.StringVar(&o.env, "env", os.Getenv("SNUGKV_SECRETS_ENV"), "")
	fs.StringVar(&o.format, "format", "dotenv", "")
	fs.DurationVar(&o.wait, "wait", 30*time.Second, "")
	fs.BoolVar(&o.override, "override", false, "")
	fs.Usage = func() { fmt.Fprint(stderr, usage) }
	if err := fs.Parse(args[1:]); err != nil {
		return 2
	}
	rest := fs.Args()
	fail := func(err error) int {
		fmt.Fprintf(stderr, "snugkv-secrets: %v\n", err)
		return 1
	}
	if o.url == "" {
		return fail(errors.New("no API address: set SNUGKV_SECRETS_URL or --url"))
	}
	if !envRe.MatchString(o.env) {
		return fail(errors.New("--env must look like project/env, e.g. airsig/prod"))
	}

	switch cmd {
	case "run":
		if len(rest) == 0 {
			return fail(errors.New("run needs a command after --"))
		}
		vals, err := fetch(o)
		if err != nil {
			return fail(err)
		}
		return run(rest, mergeEnv(os.Environ(), vals, o.override), stderr)
	case "export":
		vals, err := fetch(o)
		if err != nil {
			return fail(err)
		}
		switch o.format {
		case "dotenv":
			fmt.Fprint(stdout, secrets.FormatDotenv(vals))
		case "json":
			b, _ := json.MarshalIndent(vals, "", "  ")
			fmt.Fprintln(stdout, string(b))
		default:
			return fail(errors.New("--format is dotenv or json"))
		}
		return 0
	case "get":
		if len(rest) != 1 {
			return fail(errors.New("get needs one secret name"))
		}
		vals, err := fetch(o)
		if err != nil {
			return fail(err)
		}
		v, ok := vals[rest[0]]
		if !ok {
			return fail(fmt.Errorf("%s is not set in %s", rest[0], o.env))
		}
		fmt.Fprintln(stdout, v)
		return 0
	}
	fmt.Fprint(stderr, usage)
	return 2
}

var envRe = regexp.MustCompile(`^[a-z0-9][a-z0-9_-]{0,47}/[a-z0-9][a-z0-9_-]{0,47}$`)

// fetch reads every secret of the environment, retrying while the API is
// unreachable (it may still be starting) but not on answers like 401 or 404.
func fetch(o options) (map[string]string, error) {
	base, err := url.Parse(strings.TrimRight(o.url, "/"))
	if err != nil || (base.Scheme != "http" && base.Scheme != "https") || base.Host == "" {
		return nil, errors.New("the API address must look like http://host:port")
	}
	endpoint := base.String() + "/v1/envs/" + o.env + "/export"
	client := &http.Client{Timeout: 10 * time.Second}
	deadline := time.Now().Add(o.wait)
	delay := 250 * time.Millisecond
	for {
		req, _ := http.NewRequest(http.MethodGet, endpoint, nil)
		if o.token != "" {
			req.Header.Set("Authorization", "Bearer "+o.token)
		}
		resp, err := client.Do(req)
		if err == nil {
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 8<<20))
			resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				var out struct {
					Values map[string]string `json:"values"`
				}
				if err := json.Unmarshal(body, &out); err != nil {
					return nil, fmt.Errorf("unexpected answer from %s", base.Host)
				}
				return out.Values, nil
			}
			var e struct {
				Error string `json:"error"`
			}
			json.Unmarshal(body, &e)
			if resp.StatusCode < 500 || time.Now().After(deadline) {
				return nil, fmt.Errorf("%s: HTTP %d %s", o.env, resp.StatusCode, e.Error)
			}
		} else if time.Now().After(deadline) {
			return nil, fmt.Errorf("cannot reach the secrets API at %s: %v", base.Host, err)
		}
		time.Sleep(delay)
		delay = min(delay*2, 3*time.Second)
	}
}

// mergeEnv adds the secrets to an environment. Variables already set win
// unless override is true. The token never reaches the child.
func mergeEnv(environ []string, vals map[string]string, override bool) []string {
	set := map[string]int{}
	out := make([]string, 0, len(environ)+len(vals))
	for _, kv := range environ {
		k, _, _ := strings.Cut(kv, "=")
		if k == "SNUGKV_SECRETS_TOKEN" {
			continue
		}
		set[k] = len(out)
		out = append(out, kv)
	}
	names := make([]string, 0, len(vals))
	for k := range vals {
		names = append(names, k)
	}
	sort.Strings(names)
	for _, k := range names {
		if i, ok := set[k]; ok {
			if override {
				out[i] = k + "=" + vals[k]
			}
			continue
		}
		out = append(out, k+"="+vals[k])
	}
	return out
}

// run starts the command, passes signals on and returns its exit code.
func run(argv []string, env []string, stderr io.Writer) int {
	c := exec.Command(argv[0], argv[1:]...)
	c.Env, c.Stdin, c.Stdout, c.Stderr = env, os.Stdin, os.Stdout, os.Stderr
	if err := c.Start(); err != nil {
		fmt.Fprintf(stderr, "snugkv-secrets: %v\n", err)
		return 127
	}
	sigs := make(chan os.Signal, 4)
	signal.Notify(sigs, syscall.SIGINT, syscall.SIGTERM, syscall.SIGHUP, syscall.SIGQUIT)
	go func() {
		for s := range sigs {
			c.Process.Signal(s)
		}
	}()
	err := c.Wait()
	signal.Stop(sigs)
	close(sigs)
	var ee *exec.ExitError
	if errors.As(err, &ee) {
		if st, ok := ee.Sys().(syscall.WaitStatus); ok && st.Signaled() {
			return 128 + int(st.Signal())
		}
		return ee.ExitCode()
	}
	if err != nil {
		return 1
	}
	return 0
}

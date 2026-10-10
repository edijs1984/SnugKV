// rpckeys manages API keys, plans and usage for rpccache -auth.
//
//	rpckeys plan set <name> -rps 10 -burst 20 -daily 100000
//	rpckeys plan list
//	rpckeys key create -label acme -plan free
//	rpckeys key list
//	rpckeys key plan <id> <plan>
//	rpckeys key revoke <id>
//	rpckeys usage <id> [-days 7]
//
// The store is SnugKV (rate limits need its snug_rate_limit function) and is
// given with -store or RPC_AUTH_STORE before the command.
package main

import (
	"flag"
	"fmt"
	"os"
	"sort"
	"strings"
	"text/tabwriter"
	"time"

	"snugkv/internal/rpccache"
)

func main() {
	store := flag.String("store", envOr("RPC_AUTH_STORE", "127.0.0.1:6379"), "SnugKV address holding keys (or RPC_AUTH_STORE)")
	flag.Usage = usage
	flag.Parse()
	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}
	c := rpccache.NewClient(*store, 2, 2*time.Second)
	defer c.Close()
	a := rpccache.NewAdmin(c)
	if err := run(a, args); err != nil {
		fmt.Fprintln(os.Stderr, "rpckeys:", err)
		os.Exit(1)
	}
}

func envOr(k, d string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return d
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: rpckeys [-store host:port] <command>

  plan set <name> -rps N -burst N [-daily N]   create or change a plan (daily 0 = unlimited)
  plan list
  key create -label TEXT -plan NAME            prints the secret once
  key list
  key plan <id> <plan>                         move a key to another plan
  key revoke <id>
  usage <id> [-days N]                         calls per day, split by cache class
`)
}

func run(a *rpccache.Admin, args []string) error {
	switch {
	case len(args) >= 2 && args[0] == "plan" && args[1] == "set":
		fs := flag.NewFlagSet("plan set", flag.ExitOnError)
		rps := fs.Float64("rps", 0, "sustained calls per second")
		burst := fs.Int("burst", 0, "bucket size; also the largest batch")
		daily := fs.Int64("daily", 0, "calls per UTC day (0 = unlimited)")
		name, rest := splitName(args[2:])
		fs.Parse(rest)
		if name == "" {
			return fmt.Errorf("plan set <name> -rps N -burst N [-daily N]")
		}
		if err := a.SetPlan(name, rpccache.Plan{RPS: *rps, Burst: *burst, Daily: *daily}); err != nil {
			return err
		}
		fmt.Printf("plan %s: %g calls/s, burst %d, daily %s\n", name, *rps, *burst, dailyStr(*daily))
	case len(args) >= 2 && args[0] == "plan" && args[1] == "list":
		plans, err := a.Plans()
		if err != nil {
			return err
		}
		names := make([]string, 0, len(plans))
		for n := range plans {
			names = append(names, n)
		}
		sort.Strings(names)
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "PLAN\tRPS\tBURST\tDAILY")
		for _, n := range names {
			p := plans[n]
			fmt.Fprintf(w, "%s\t%g\t%d\t%s\n", n, p.RPS, p.Burst, dailyStr(p.Daily))
		}
		w.Flush()
	case len(args) >= 2 && args[0] == "key" && args[1] == "create":
		fs := flag.NewFlagSet("key create", flag.ExitOnError)
		label := fs.String("label", "", "who the key is for")
		plan := fs.String("plan", "", "plan name")
		fs.Parse(args[2:])
		if *plan == "" {
			return fmt.Errorf("key create -label TEXT -plan NAME")
		}
		secret, rec, err := a.CreateKey(*label, *plan)
		if err != nil {
			return err
		}
		fmt.Printf("id:     %s\nplan:   %s\nsecret: %s\n\nThe secret is shown once and cannot be recovered.\n", rec.ID, rec.Plan, secret)
	case len(args) >= 2 && args[0] == "key" && args[1] == "list":
		keys, err := a.Keys()
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "ID\tLABEL\tPLAN\tCREATED\tSTATE")
		for _, k := range keys {
			state := "active"
			if k.Revoked {
				state = "revoked"
			}
			fmt.Fprintf(w, "%s\t%s\t%s\t%s\t%s\n", k.ID, k.Label, k.Plan, time.Unix(k.Created, 0).UTC().Format("2006-01-02"), state)
		}
		w.Flush()
	case len(args) == 4 && args[0] == "key" && args[1] == "plan":
		if err := a.SetKeyPlan(args[2], args[3]); err != nil {
			return err
		}
		fmt.Println("ok; running proxies pick it up within their record TTL (default 5s)")
	case len(args) == 3 && args[0] == "key" && args[1] == "revoke":
		if err := a.Revoke(args[2]); err != nil {
			return err
		}
		fmt.Println("revoked; running proxies stop accepting it within their record TTL (default 5s)")
	case len(args) >= 2 && args[0] == "usage":
		fs := flag.NewFlagSet("usage", flag.ExitOnError)
		days := fs.Int("days", 7, "days to show, today first")
		id, rest := splitName(args[1:])
		fs.Parse(rest)
		if id == "" {
			return fmt.Errorf("usage <id> [-days N]")
		}
		u, err := a.Usage(id, *days)
		if err != nil {
			return err
		}
		w := tabwriter.NewWriter(os.Stdout, 0, 4, 2, ' ', 0)
		fmt.Fprintln(w, "DAY\tCALLS\tIMMUTABLE\tSTATIC\tRECENT\tSTATE\tTIP\tBYPASS\tDENIED")
		for _, d := range u {
			c := d.Counters
			fmt.Fprintf(w, "%s\t%d\t%d\t%d\t%d\t%d\t%d\t%d\t%d\n", d.Day, c["calls"], c["immutable"], c["static"], c["recent"], c["state"], c["tip"], c["bypass"], c["denied"])
		}
		w.Flush()
	default:
		usage()
		return fmt.Errorf("unknown command %q", strings.Join(args, " "))
	}
	return nil
}

// splitName takes a leading positional argument so flags may follow it.
func splitName(args []string) (string, []string) {
	if len(args) > 0 && !strings.HasPrefix(args[0], "-") {
		return args[0], args[1:]
	}
	return "", args
}

func dailyStr(d int64) string {
	if d == 0 {
		return "unlimited"
	}
	return fmt.Sprint(d)
}

// Package rpccache is a caching JSON-RPC proxy for blockchain nodes. It sits
// between applications and an RPC provider, serves repeated reads from a
// Redis-protocol cache (SnugKV or Redis), and forwards everything else.
package rpccache

import (
	"bytes"
	"encoding/json"
	"strings"
	"time"
)

// Class says how long a response stays valid.
type Class uint8

const (
	// Bypass is never cached: writes, simulations, unknown methods, and reads
	// that ask for a minimum slot.
	Bypass Class = iota
	// Immutable results cannot change once they exist (a finalized transaction,
	// a block by hash). They are kept for TTLs.Immutable.
	Immutable
	// Static results change rarely (genesis hash, chain id).
	Static
	// Recent results address a specific old-but-reorgable height.
	Recent
	// State results track the chain head and expire quickly (account data).
	State
	// Tip results are the chain head itself (current slot, latest block).
	Tip
)

func (c Class) String() string {
	return [...]string{"bypass", "immutable", "static", "recent", "state", "tip"}[c]
}

// TTLs holds the validity of each class.
type TTLs struct {
	Immutable, Static, Recent, State, Tip time.Duration
}

// DefaultTTLs are chosen so a cached answer is never more than about one
// block behind: Solana slots are 400 ms, Ethereum blocks 12 s.
func DefaultTTLs() TTLs {
	return TTLs{
		Immutable: 24 * time.Hour,
		Static:    5 * time.Minute,
		Recent:    15 * time.Second,
		State:     time.Second,
		Tip:       200 * time.Millisecond,
	}
}

func (t TTLs) For(c Class) time.Duration {
	switch c {
	case Immutable:
		return t.Immutable
	case Static:
		return t.Static
	case Recent:
		return t.Recent
	case State:
		return t.State
	case Tip:
		return t.Tip
	}
	return 0
}

// Chain selects a method table.
type Chain string

const (
	Solana Chain = "solana"
	EVM    Chain = "evm"
)

var solanaState = set(
	"getAccountInfo", "getBalance", "getMultipleAccounts", "getTokenAccountBalance",
	"getTokenAccountsByOwner", "getTokenAccountsByDelegate", "getTokenLargestAccounts",
	"getTokenSupply", "getProgramAccounts", "getSignaturesForAddress",
	"getSignatureStatuses", "getVoteAccounts", "getLargestAccounts", "getSupply",
)

var solanaTip = set(
	"getSlot", "getBlockHeight", "getLatestBlockhash", "getEpochInfo",
	"getRecentPrioritizationFees", "getBlockTime", "getTransactionCount",
)

var solanaStatic = set(
	"getGenesisHash", "getEpochSchedule", "getMinimumBalanceForRentExemption", "getVersion",
)

// Classify returns how a request may be cached. params is the raw "params"
// member of the request.
func Classify(chain Chain, method string, params json.RawMessage) Class {
	switch chain {
	case EVM:
		return classifyEVM(method, params)
	default:
		return classifySolana(method, params)
	}
}

func classifySolana(method string, params json.RawMessage) Class {
	cfg := lastObject(params)
	if _, ok := cfg["minContextSlot"]; ok {
		return Bypass
	}
	commitment, _ := cfg["commitment"].(string)

	switch method {
	case "getTransaction", "getBlock":
		// Both default to finalized. A finalized result is permanent;
		// anything weaker can still be rolled back.
		if commitment == "" || commitment == "finalized" {
			return Immutable
		}
		return State
	}
	switch {
	case solanaState[method]:
		return State
	case solanaTip[method]:
		return Tip
	case solanaStatic[method]:
		return Static
	}
	return Bypass
}

func classifyEVM(method string, params json.RawMessage) Class {
	switch method {
	case "eth_chainId", "net_version":
		return Static
	case "eth_getTransactionByHash", "eth_getTransactionReceipt",
		"eth_getBlockByHash", "eth_getBlockTransactionCountByHash",
		"eth_getTransactionByBlockHashAndIndex", "eth_getUncleByBlockHashAndIndex":
		// By hash: the result either does not exist yet (null, never cached)
		// or is the data for that hash. A reorg can drop a recent block, so use
		// the Recent TTL rather than Immutable for transactions and receipts.
		return Recent
	case "eth_blockNumber", "eth_gasPrice", "eth_maxPriorityFeePerGas", "eth_feeHistory":
		return Tip
	case "eth_getBalance", "eth_getCode", "eth_getStorageAt", "eth_getTransactionCount", "eth_call":
		return evmByBlockTag(params, 1)
	case "eth_getBlockByNumber", "eth_getBlockTransactionCountByNumber":
		return evmByBlockTag(params, 0)
	case "eth_getLogs":
		return evmLogs(params)
	}
	return Bypass
}

// evmByBlockTag classifies by the block parameter at index i: "latest" and its
// relatives are head data, a number or hash is a fixed point in the past.
func evmByBlockTag(params json.RawMessage, i int) Class {
	var arr []json.RawMessage
	if json.Unmarshal(params, &arr) != nil || i >= len(arr) {
		return Bypass
	}
	var tag string
	if json.Unmarshal(arr[i], &tag) != nil {
		// A {blockHash: ...} or {blockNumber: ...} object.
		return Recent
	}
	switch tag {
	case "latest", "pending", "safe", "earliest":
		if tag == "pending" {
			return Bypass
		}
		return State
	case "finalized":
		return Recent
	}
	if strings.HasPrefix(tag, "0x") {
		return Recent
	}
	return Bypass
}

func evmLogs(params json.RawMessage) Class {
	var arr []map[string]any
	if json.Unmarshal(params, &arr) != nil || len(arr) != 1 {
		return Bypass
	}
	f := arr[0]
	if _, ok := f["blockHash"]; ok {
		return Recent
	}
	from, _ := f["fromBlock"].(string)
	to, _ := f["toBlock"].(string)
	if strings.HasPrefix(from, "0x") && strings.HasPrefix(to, "0x") {
		return Recent
	}
	return Bypass
}

func set(names ...string) map[string]bool {
	m := make(map[string]bool, len(names))
	for _, n := range names {
		m[n] = true
	}
	return m
}

// lastObject returns the final params element when it is a JSON object (the
// Solana config argument), else nil.
func lastObject(params json.RawMessage) map[string]any {
	params = bytes.TrimSpace(params)
	if len(params) == 0 || params[0] != '[' {
		return nil
	}
	var arr []json.RawMessage
	if json.Unmarshal(params, &arr) != nil || len(arr) == 0 {
		return nil
	}
	last := bytes.TrimSpace(arr[len(arr)-1])
	if len(last) == 0 || last[0] != '{' {
		return nil
	}
	var obj map[string]any
	if json.Unmarshal(last, &obj) != nil {
		return nil
	}
	return obj
}

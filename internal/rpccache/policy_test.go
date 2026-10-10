package rpccache

import (
	"encoding/json"
	"testing"
)

func TestClassify(t *testing.T) {
	for _, tc := range []struct {
		chain  Chain
		method string
		params string
		want   Class
	}{
		{Solana, "getAccountInfo", `["Tokenkeg",{"encoding":"base64"}]`, State},
		{Solana, "getAccountInfo", `["Tokenkeg",{"minContextSlot":5}]`, Bypass},
		{Solana, "getBalance", `["abc"]`, State},
		{Solana, "getTransaction", `["sig"]`, Immutable},
		{Solana, "getTransaction", `["sig",{"commitment":"finalized"}]`, Immutable},
		{Solana, "getTransaction", `["sig",{"commitment":"confirmed"}]`, State},
		{Solana, "getBlock", `[123,{"commitment":"processed"}]`, State},
		{Solana, "getBlock", `[123]`, Immutable},
		{Solana, "getSlot", `[]`, Tip},
		{Solana, "getLatestBlockhash", `[{"commitment":"finalized"}]`, Tip},
		{Solana, "getGenesisHash", ``, Static},
		{Solana, "sendTransaction", `["tx"]`, Bypass},
		{Solana, "simulateTransaction", `["tx"]`, Bypass},
		{Solana, "requestAirdrop", `["a",1]`, Bypass},
		{Solana, "somethingNew", `[]`, Bypass},
		{EVM, "eth_chainId", `[]`, Static},
		{EVM, "eth_blockNumber", `[]`, Tip},
		{EVM, "eth_getBalance", `["0xabc","latest"]`, State},
		{EVM, "eth_getBalance", `["0xabc","0x10"]`, Recent},
		{EVM, "eth_getBalance", `["0xabc","pending"]`, Bypass},
		{EVM, "eth_getBalance", `["0xabc","finalized"]`, Recent},
		{EVM, "eth_call", `[{"to":"0x1"},"latest"]`, State},
		{EVM, "eth_call", `[{"to":"0x1"},"0x1b4"]`, Recent},
		{EVM, "eth_getTransactionReceipt", `["0xhash"]`, Recent},
		{EVM, "eth_getBlockByNumber", `["latest",false]`, State},
		{EVM, "eth_getBlockByNumber", `["0x10",false]`, Recent},
		{EVM, "eth_getLogs", `[{"fromBlock":"0x1","toBlock":"0x9"}]`, Recent},
		{EVM, "eth_getLogs", `[{"fromBlock":"0x1","toBlock":"latest"}]`, Bypass},
		{EVM, "eth_sendRawTransaction", `["0x"]`, Bypass},
		{EVM, "eth_estimateGas", `[{}]`, Bypass},
	} {
		got := Classify(tc.chain, tc.method, json.RawMessage(tc.params))
		if got != tc.want {
			t.Errorf("%s %s %s: %v, want %v", tc.chain, tc.method, tc.params, got, tc.want)
		}
	}
}

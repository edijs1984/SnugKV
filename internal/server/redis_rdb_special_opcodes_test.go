package server

import (
	"encoding/binary"
	"strings"
	"testing"
)

func finalizeRedisFullSyncFixture(body []byte) []byte {
	body = append(body, redisRDBOpcodeEOF)
	var checksum [8]byte
	binary.LittleEndian.PutUint64(checksum[:], redisCRC64(body))
	return append(body, checksum[:]...)
}

func TestDecodeRedisFullSyncRDBFunction2(t *testing.T) {
	code := "#!lua name=fromredis\nredis.register_function('hello', function() return 'ok' end)"
	rdb := []byte("REDIS0012")
	rdb = append(rdb, redisRDBOpcodeFunction2)
	rdb = appendRDBRawString(rdb, []byte(code))
	rdb = finalizeRedisFullSyncFixture(rdb)

	decoded, err := decodeRedisFullSyncRDBState(rdb)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.functionCodes) != 1 || decoded.functionCodes[0] != code {
		t.Fatalf("function codes=%q", decoded.functionCodes)
	}
	if len(decoded.records) != 1 || !decoded.records[0].Reset {
		t.Fatalf("records=%+v", decoded.records)
	}
}

func TestDecodeRedisFullSyncRDBSlotInfoIgnored(t *testing.T) {
	rdb := []byte("REDIS0012")
	rdb = append(rdb, redisRDBOpcodeSelectDB)
	rdb = appendRDBLen(rdb, 0)
	rdb = append(rdb, redisRDBOpcodeSlotInfo)
	rdb = appendRDBLen(rdb, 123)
	rdb = appendRDBLen(rdb, 2)
	rdb = appendRDBLen(rdb, 1)
	rdb = append(rdb, keyRDBTypeString)
	rdb = appendRDBRawString(rdb, []byte("cluster:key"))
	rdb = appendRDBRawString(rdb, []byte("value"))
	rdb = finalizeRedisFullSyncFixture(rdb)

	decoded, err := decodeRedisFullSyncRDBState(rdb)
	if err != nil {
		t.Fatal(err)
	}
	if len(decoded.records) != 2 || string(decoded.records[1].Key) != "cluster:key" {
		t.Fatalf("records=%+v", decoded.records)
	}
}

func TestDecodeRedisFullSyncRDBRejectsUnsupportedSpecialOpcodes(t *testing.T) {
	for _, tc := range []struct {
		name   string
		opcode byte
		want   string
	}{
		{name: "function-pre-ga", opcode: redisRDBOpcodeFunctionPreGA, want: "pre-GA function"},
		{name: "module-aux", opcode: redisRDBOpcodeModuleAux, want: "module aux"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			rdb := finalizeRedisFullSyncFixture(append([]byte("REDIS0012"), tc.opcode))
			_, err := decodeRedisFullSyncRDBState(rdb)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("err=%v want substring %q", err, tc.want)
			}
		})
	}
}

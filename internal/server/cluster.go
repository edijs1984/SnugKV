package server

import (
	"errors"
	"fmt"
	"net"
	"strconv"
	"strings"
)

const clusterSlotCount = 16384

func clusterHashKey(key []byte) []byte {
	start := -1
	for i, b := range key {
		if b == '{' {
			start = i + 1
			break
		}
	}
	if start < 0 || start >= len(key) {
		return key
	}
	for i := start; i < len(key); i++ {
		if key[i] == '}' {
			if i == start {
				return key
			}
			return key[start:i]
		}
	}
	return key
}

func clusterCRC16(data []byte) uint16 {
	var crc uint16
	for _, b := range data {
		crc ^= uint16(b) << 8
		for i := 0; i < 8; i++ {
			if crc&0x8000 != 0 {
				crc = (crc << 1) ^ 0x1021
			} else {
				crc <<= 1
			}
		}
	}
	return crc
}

func clusterKeySlot(key []byte) int {
	return int(clusterCRC16(clusterHashKey(key)) & (clusterSlotCount - 1))
}

func parseClusterSlotRange(text string) (int, int, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return 0, 0, errors.New("empty cluster slot range")
	}
	if !strings.Contains(text, "-") {
		slot, err := strconv.Atoi(text)
		if err != nil || slot < 0 || slot >= clusterSlotCount {
			return 0, 0, fmt.Errorf("invalid cluster slot %q", text)
		}
		return slot, slot, nil
	}
	parts := strings.Split(text, "-")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("invalid cluster slot range %q", text)
	}
	start, err := strconv.Atoi(strings.TrimSpace(parts[0]))
	if err != nil {
		return 0, 0, fmt.Errorf("invalid cluster slot range %q", text)
	}
	end, err := strconv.Atoi(strings.TrimSpace(parts[1]))
	if err != nil || start < 0 || end < 0 || start >= clusterSlotCount || end >= clusterSlotCount || start > end {
		return 0, 0, fmt.Errorf("invalid cluster slot range %q", text)
	}
	return start, end, nil
}

func (s *Server) configureClusterSlots(enabled bool, nodeAddr string, ranges map[string]string) error {
	s.clusterEnabled = enabled
	s.clusterNodeAddr = nodeAddr
	for i := range s.clusterSlotOwners {
		s.clusterSlotOwners[i] = ""
	}
	for rangeText, owner := range ranges {
		start, end, err := parseClusterSlotRange(rangeText)
		if err != nil {
			return err
		}
		for slot := start; slot <= end; slot++ {
			if s.clusterSlotOwners[slot] != "" {
				return fmt.Errorf("cluster slot %d has multiple owners", slot)
			}
			s.clusterSlotOwners[slot] = owner
		}
	}
	return nil
}


func (s *Server) enforceClusterRouting(args [][]byte) error {
	if !s.clusterEnabled {
		return nil
	}
	if len(args) == 0 || strings.EqualFold(string(args[0]), "CLUSTER") {
		return nil
	}

	keys, err := commandKeys(args)
	if err != nil {
		return err
	}
	if len(keys) == 0 {
		return nil
	}

	slot := clusterKeySlot(keys[0].value)
	for _, key := range keys[1:] {
		if clusterKeySlot(key.value) != slot {
			return errors.New("CROSSSLOT Keys in request don't hash to the same slot")
		}
	}

	owner := s.clusterSlotOwners[slot]
	if owner == "" {
		return errors.New("CLUSTERDOWN Hash slot not served")
	}
	if owner != s.clusterNodeAddr {
		return fmt.Errorf("MOVED %d %s", slot, owner)
	}
	return nil
}


type clusterSlotRange struct {
	Start int
	End   int
	Owner string
}

func (s *Server) clusterSlotRanges() []clusterSlotRange {
	out := make([]clusterSlotRange, 0)
	start := -1
	owner := ""
	for slot := 0; slot < clusterSlotCount; slot++ {
		current := s.clusterSlotOwners[slot]
		if current == owner {
			continue
		}
		if owner != "" {
			out = append(out, clusterSlotRange{Start: start, End: slot - 1, Owner: owner})
		}
		owner = current
		start = slot
	}
	if owner != "" {
		out = append(out, clusterSlotRange{Start: start, End: clusterSlotCount - 1, Owner: owner})
	}
	return out
}

func clusterNodeEndpointReply(addr string) ([]byte, error) {
	host, portText, err := net.SplitHostPort(addr)
	if err != nil {
		return nil, err
	}
	port, err := strconv.Atoi(portText)
	if err != nil {
		return nil, err
	}
	return array(
		formatBulkString([]byte(host)),
		integer(int64(port)),
		formatBulkString([]byte(addr)),
	), nil
}

func (s *Server) clusterSlotsReply() ([]byte, error) {
	ranges := s.clusterSlotRanges()
	items := make([][]byte, 0, len(ranges))
	for _, r := range ranges {
		node, err := clusterNodeEndpointReply(r.Owner)
		if err != nil {
			return nil, err
		}
		items = append(items, array(
			integer(int64(r.Start)),
			integer(int64(r.End)),
			node,
		))
	}
	return array(items...), nil
}

func (s *Server) clusterShardsReply() ([]byte, error) {
	ranges := s.clusterSlotRanges()
	items := make([][]byte, 0, len(ranges))
	for _, r := range ranges {
		host, portText, err := net.SplitHostPort(r.Owner)
		if err != nil {
			return nil, err
		}
		port, err := strconv.Atoi(portText)
		if err != nil {
			return nil, err
		}
		node := array(
			formatBulkString([]byte("id")),
			formatBulkString([]byte(r.Owner)),
			formatBulkString([]byte("endpoint")),
			formatBulkString([]byte(host)),
			formatBulkString([]byte("ip")),
			formatBulkString([]byte(host)),
			formatBulkString([]byte("port")),
			integer(int64(port)),
			formatBulkString([]byte("role")),
			formatBulkString([]byte("master")),
			formatBulkString([]byte("health")),
			formatBulkString([]byte("online")),
		)
		shard := array(
			formatBulkString([]byte("slots")),
			array(integer(int64(r.Start)), integer(int64(r.End))),
			formatBulkString([]byte("nodes")),
			array(node),
		)
		items = append(items, shard)
	}
	return array(items...), nil
}

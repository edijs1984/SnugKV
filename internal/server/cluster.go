package server

import (
	"errors"
	"fmt"
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

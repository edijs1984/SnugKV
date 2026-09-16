package server

import (
	"errors"
	"snugkv/internal/engine"
	"strings"
)

func init() {
	commandTable["XINFO"] = commandInfo{2, 0, 0, 0, 0, false}
}

func isStreamInfoCommand(args [][]byte) bool {
	return len(args) > 0 && strings.EqualFold(string(args[0]), "XINFO")
}

func streamInfoNullableInt(value *int64) []byte {
	if value == nil {
		return nullBulk()
	}
	return integer(*value)
}

func streamInfoEntryResponse(entry *engine.StreamEntry) []byte {
	if entry == nil {
		return nullBulk()
	}
	fields := make([][]byte, 0, len(entry.Fields)*2)
	for _, pair := range entry.Fields {
		fields = append(fields, formatBulkString(pair.Field), formatBulkString(pair.Value))
	}
	return array(
		formatBulkString([]byte(entry.ID.String())),
		array(fields...),
	)
}

func streamInfoPendingResponse(item engine.StreamInfoPending, includeConsumer bool) []byte {
	parts := make([][]byte, 0, 4)
	parts = append(parts, formatBulkString([]byte(item.ID.String())))
	if includeConsumer {
		parts = append(parts, formatBulkString([]byte(item.Consumer)))
	}
	parts = append(parts, integer(item.DeliveredAt), integer(int64(item.Deliveries)))
	return array(parts...)
}

func streamInfoConsumerFullResponse(consumer engine.StreamInfoConsumer) []byte {
	pending := make([][]byte, 0, len(consumer.PendingEntries))
	for _, item := range consumer.PendingEntries {
		pending = append(pending, streamInfoPendingResponse(item, false))
	}
	return array(
		formatBulkString([]byte("name")), formatBulkString([]byte(consumer.Name)),
		formatBulkString([]byte("seen-time")), integer(consumer.SeenTime),
		formatBulkString([]byte("active-time")), integer(consumer.ActiveTime),
		formatBulkString([]byte("pel-count")), integer(consumer.Pending),
		formatBulkString([]byte("pending")), array(pending...),
	)
}

func streamInfoGroupResponse(group engine.StreamInfoGroup, full bool) []byte {
	if !full {
		return array(
			formatBulkString([]byte("name")), formatBulkString([]byte(group.Name)),
			formatBulkString([]byte("consumers")), integer(group.Consumers),
			formatBulkString([]byte("pending")), integer(group.Pending),
			formatBulkString([]byte("last-delivered-id")), formatBulkString([]byte(group.LastDeliveredID.String())),
			formatBulkString([]byte("entries-read")), streamInfoNullableInt(group.EntriesRead),
			formatBulkString([]byte("lag")), streamInfoNullableInt(group.Lag),
		)
	}
	pending := make([][]byte, 0, len(group.PendingEntries))
	for _, item := range group.PendingEntries {
		pending = append(pending, streamInfoPendingResponse(item, true))
	}
	consumers := make([][]byte, 0, len(group.ConsumerInfos))
	for _, consumer := range group.ConsumerInfos {
		consumers = append(consumers, streamInfoConsumerFullResponse(consumer))
	}
	return array(
		formatBulkString([]byte("name")), formatBulkString([]byte(group.Name)),
		formatBulkString([]byte("last-delivered-id")), formatBulkString([]byte(group.LastDeliveredID.String())),
		formatBulkString([]byte("entries-read")), streamInfoNullableInt(group.EntriesRead),
		formatBulkString([]byte("lag")), streamInfoNullableInt(group.Lag),
		formatBulkString([]byte("pel-count")), integer(group.Pending),
		formatBulkString([]byte("pending")), array(pending...),
		formatBulkString([]byte("consumers")), array(consumers...),
	)
}

func streamInfoSummaryResponse(info engine.StreamInfoResult) []byte {
	return array(
		formatBulkString([]byte("length")), integer(info.Length),
		formatBulkString([]byte("radix-tree-keys")), integer(info.RadixTreeKeys),
		formatBulkString([]byte("radix-tree-nodes")), integer(info.RadixTreeNodes),
		formatBulkString([]byte("last-generated-id")), formatBulkString([]byte(info.LastGeneratedID.String())),
		formatBulkString([]byte("max-deleted-entry-id")), formatBulkString([]byte(info.MaxDeletedEntryID.String())),
		formatBulkString([]byte("entries-added")), integer(info.EntriesAdded),
		formatBulkString([]byte("recorded-first-entry-id")), formatBulkString([]byte(info.RecordedFirstEntryID.String())),
		formatBulkString([]byte("groups")), integer(info.Groups),
		formatBulkString([]byte("first-entry")), streamInfoEntryResponse(info.FirstEntry),
		formatBulkString([]byte("last-entry")), streamInfoEntryResponse(info.LastEntry),
	)
}

func streamInfoFullResponse(info engine.StreamInfoResult) []byte {
	entries := make([][]byte, 0, len(info.Entries))
	for i := range info.Entries {
		entry := info.Entries[i]
		entries = append(entries, streamInfoEntryResponse(&entry))
	}
	groups := make([][]byte, 0, len(info.GroupInfos))
	for _, group := range info.GroupInfos {
		groups = append(groups, streamInfoGroupResponse(group, true))
	}
	return array(
		formatBulkString([]byte("length")), integer(info.Length),
		formatBulkString([]byte("radix-tree-keys")), integer(info.RadixTreeKeys),
		formatBulkString([]byte("radix-tree-nodes")), integer(info.RadixTreeNodes),
		formatBulkString([]byte("last-generated-id")), formatBulkString([]byte(info.LastGeneratedID.String())),
		formatBulkString([]byte("max-deleted-entry-id")), formatBulkString([]byte(info.MaxDeletedEntryID.String())),
		formatBulkString([]byte("entries-added")), integer(info.EntriesAdded),
		formatBulkString([]byte("recorded-first-entry-id")), formatBulkString([]byte(info.RecordedFirstEntryID.String())),
		formatBulkString([]byte("entries")), array(entries...),
		formatBulkString([]byte("groups")), array(groups...),
	)
}

func (s *Server) executeStreamInfo(args [][]byte) ([]byte, error) {
	if len(args) < 2 {
		return nil, errors.New("ERR wrong number of arguments for 'xinfo' command")
	}
	subcommand := strings.ToUpper(string(args[1]))
	switch subcommand {
	case "HELP":
		if len(args) != 2 {
			return nil, errors.New("ERR wrong number of arguments for 'xinfo|help' command")
		}
		return array(
			formatBulkString([]byte("XINFO <subcommand> [<arg> [value] [opt] ...]. Subcommands are:")),
			formatBulkString([]byte("CONSUMERS <key> <groupname>")),
			formatBulkString([]byte("    Show consumers of <groupname>.")),
			formatBulkString([]byte("GROUPS <key>")),
			formatBulkString([]byte("    Show the stream consumer groups.")),
			formatBulkString([]byte("STREAM <key> [FULL [COUNT <count>]]")),
			formatBulkString([]byte("    Show information about the stream.")),
			formatBulkString([]byte("HELP")),
			formatBulkString([]byte("    Prints this help.")),
		), nil

	case "STREAM":
		if len(args) != 3 && len(args) != 4 && len(args) != 6 {
			return nil, errors.New("ERR syntax error")
		}
		full := false
		count := 10
		if len(args) >= 4 {
			if !strings.EqualFold(string(args[3]), "FULL") {
				return nil, errors.New("ERR syntax error")
			}
			full = true
		}
		if len(args) == 6 {
			if !strings.EqualFold(string(args[4]), "COUNT") {
				return nil, errors.New("ERR syntax error")
			}
			parsed, err := parseNonNegativeInt(args[5])
			if err != nil {
				return nil, err
			}
			count = parsed
		}
		info, err := s.store.StreamInfo(string(args[2]), full, count)
		if err != nil {
			return nil, err
		}
		if full {
			return streamInfoFullResponse(info), nil
		}
		return streamInfoSummaryResponse(info), nil

	case "GROUPS":
		if len(args) != 3 {
			return nil, errors.New("ERR wrong number of arguments for 'xinfo|groups' command")
		}
		groups, err := s.store.StreamGroupsInfo(string(args[2]))
		if err != nil {
			return nil, err
		}
		rows := make([][]byte, 0, len(groups))
		for _, group := range groups {
			rows = append(rows, streamInfoGroupResponse(group, false))
		}
		return array(rows...), nil

	case "CONSUMERS":
		if len(args) != 4 {
			return nil, errors.New("ERR wrong number of arguments for 'xinfo|consumers' command")
		}
		consumers, err := s.store.StreamConsumersInfo(string(args[2]), string(args[3]))
		if err != nil {
			return nil, err
		}
		rows := make([][]byte, 0, len(consumers))
		for _, consumer := range consumers {
			rows = append(rows, array(
				formatBulkString([]byte("name")), formatBulkString([]byte(consumer.Name)),
				formatBulkString([]byte("pending")), integer(consumer.Pending),
				formatBulkString([]byte("idle")), integer(consumer.IdleMillis),
				formatBulkString([]byte("inactive")), integer(consumer.InactiveMillis),
			))
		}
		return array(rows...), nil
	default:
		return nil, errors.New("ERR unknown subcommand")
	}
}

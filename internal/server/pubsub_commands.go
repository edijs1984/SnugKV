package server

import (
	"errors"
	"strings"
)

var pubSubCommands = map[string]commandInfo{
	"PUBLISH":       {3, 3, 0, 0, 0, false},
	"SPUBLISH":      {3, 3, 0, 0, 0, false},
	"PUBSUB":        {2, 0, 0, 0, 0, false},
	"SUBSCRIBE":     {2, 0, 0, 0, 0, false},
	"UNSUBSCRIBE":   {1, 0, 0, 0, 0, false},
	"PSUBSCRIBE":    {2, 0, 0, 0, 0, false},
	"PUNSUBSCRIBE":  {1, 0, 0, 0, 0, false},
	"SSUBSCRIBE":    {2, 0, 0, 0, 0, false},
	"SUNSUBSCRIBE":  {1, 0, 0, 0, 0, false},
	"RESET":         {1, 1, 0, 0, 0, false},
}

func init() {
	for name, info := range pubSubCommands {
		commandTable[name] = info
	}
}

func isPubSubServerCommand(args [][]byte) bool {
	if len(args) == 0 {
		return false
	}
	switch strings.ToUpper(string(args[0])) {
	case "PUBLISH", "SPUBLISH", "PUBSUB":
		return true
	default:
		return false
	}
}

func formatPubSubChannels(channels [][]byte) []byte {
	items := make([][]byte, 0, len(channels))
	for _, channel := range channels {
		items = append(items, formatBulkString(channel))
	}
	return array(items...)
}

func (s *Server) executePubSubServer(args [][]byte) ([]byte, error) {
	if len(args) == 0 {
		return nil, errors.New("ERR empty command")
	}
	cmd := strings.ToUpper(string(args[0]))
	switch cmd {
	case "PUBLISH":
		if len(args) != 3 {
			return nil, errors.New("ERR wrong number of arguments for 'publish' command")
		}
		return integer(pubSubHubForServer(s).publish(args[1], args[2])), nil

	case "SPUBLISH":
		if len(args) != 3 {
			return nil, errors.New("ERR wrong number of arguments for 'spublish' command")
		}
		return integer(pubSubHubForServer(s).publishShard(args[1], args[2])), nil

	case "PUBSUB":
		if len(args) < 2 {
			return nil, errors.New("ERR wrong number of arguments for 'pubsub' command")
		}
		hub := pubSubHubForServer(s)
		subcommand := strings.ToUpper(string(args[1]))
		switch subcommand {
		case "CHANNELS", "SHARDCHANNELS":
			if len(args) != 2 && len(args) != 3 {
				return nil, errors.New("ERR wrong number of arguments for 'pubsub|" + strings.ToLower(subcommand) + "' command")
			}
			var pattern []byte
			hasPattern := len(args) == 3
			if hasPattern {
				pattern = args[2]
			}
			if subcommand == "SHARDCHANNELS" {
				return formatPubSubChannels(hub.shardChannelsMatching(pattern, hasPattern)), nil
			}
			return formatPubSubChannels(hub.channelsMatching(pattern, hasPattern)), nil

		case "NUMSUB", "SHARDNUMSUB":
			items := make([][]byte, 0, (len(args)-2)*2)
			for _, channel := range args[2:] {
				count := hub.subscriberCount(channel)
				if subcommand == "SHARDNUMSUB" {
					count = hub.shardSubscriberCount(channel)
				}
				items = append(items, formatBulkString(channel), integer(count))
			}
			return array(items...), nil

		case "NUMPAT":
			if len(args) != 2 {
				return nil, errors.New("ERR wrong number of arguments for 'pubsub|numpat' command")
			}
			return integer(hub.patternCount()), nil

		case "HELP":
			if len(args) != 2 {
				return nil, errors.New("ERR wrong number of arguments for 'pubsub|help' command")
			}
			return array(
				formatBulkString([]byte("PUBSUB <subcommand> [<arg> [value] [opt] ...]. Subcommands are:")),
				formatBulkString([]byte("CHANNELS [<pattern>]")),
				formatBulkString([]byte("    Return the currently active channels matching a pattern.")),
				formatBulkString([]byte("NUMPAT")),
				formatBulkString([]byte("    Return the number of active pattern subscriptions.")),
				formatBulkString([]byte("NUMSUB [<channel> ...]")),
				formatBulkString([]byte("    Return the number of subscribers for the specified channels.")),
				formatBulkString([]byte("SHARDCHANNELS [<pattern>]")),
				formatBulkString([]byte("    Return the currently active shard channels matching a pattern.")),
				formatBulkString([]byte("SHARDNUMSUB [<shardchannel> ...]")),
				formatBulkString([]byte("    Return the number of subscribers for the specified shard channels.")),
				formatBulkString([]byte("HELP")),
				formatBulkString([]byte("    Prints this help.")),
			), nil
		default:
			return nil, errors.New("ERR unknown subcommand")
		}
	}
	return nil, errors.New("ERR unknown Pub/Sub command")
}

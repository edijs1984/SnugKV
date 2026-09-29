package server

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"net"
	"strings"
)

func internalControlCommand(args [][]byte) bool {
	if len(args) >= 3 && strings.EqualFold(string(args[0]), "CLUSTER") {
		switch strings.ToUpper(string(args[1])) {
		case "FAILOVER-OWNER":
			return true
		case "REBALANCE":
			return strings.EqualFold(string(args[2]), "EXECUTE")
		case "MEMBERSHIP":
			switch strings.ToUpper(string(args[2])) {
			case "VIEW", "CHECK", "ADD", "REMOVE":
				return true
			}
		}
	}
	if len(args) >= 2 && strings.EqualFold(string(args[0]), "SNUG.FAILOVER") {
		switch strings.ToUpper(string(args[1])) {
		case "STATE",
			"REQUESTVOTE",
			"LEASE",
			"REPARENT",
			"DEMOTE",
			"MEMBERSHIPPREPARE",
			"MEMBERSHIPCOMMIT",
			"MEMBERSHIPABORT",
			"RETIREPREPARE",
			"RETIRE":
			return true
		}
	}
	return false
}

func optionalInternalControlSecret(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func internalControlCredentialMatches(configured string, supplied []byte) bool {
	if configured == "" || len(configured) != len(supplied) {
		return false
	}
	return subtle.ConstantTimeCompare([]byte(configured), supplied) == 1
}

func authenticateInternalControlUpstream(
	conn net.Conn,
	reader *bufio.Reader,
	secret string,
) error {
	if secret == "" {
		return nil
	}
	if err := writeReplicationRESPCommand(conn, "SNUG.INTERNAL", "AUTH", secret); err != nil {
		return err
	}
	line, err := readMigrateLine(reader)
	if err != nil {
		return err
	}
	if line != "+OK" {
		if strings.HasPrefix(line, "-") {
			return errors.New(strings.TrimPrefix(line, "-"))
		}
		return errors.New("unexpected internal cluster control authentication response")
	}
	return nil
}

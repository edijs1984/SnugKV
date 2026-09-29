package server

import (
	"bufio"
	"crypto/subtle"
	"errors"
	"net"
	"strings"
)

func (s *Server) executeInternalClusterControlAuth(
	client *clientSession,
	auth *authSession,
	args [][]byte,
) ([]byte, error) {
	if len(args) != 3 || !strings.EqualFold(string(args[1]), "AUTH") {
		return nil, errors.New("ERR syntax error")
	}
	if s.clusterControlAuth == "" {
		return nil, errors.New("ERR internal cluster control authentication is disabled")
	}
	if auth == nil || !auth.authenticated {
		return nil, errors.New("NOAUTH Authentication required.")
	}
	if subtle.ConstantTimeCompare(args[2], []byte(s.clusterControlAuth)) != 1 {
		return nil, errors.New("WRONGPASS invalid internal cluster control credentials")
	}
	if client == nil {
		return nil, errors.New("ERR internal cluster control requires a client session")
	}
	client.setInternalClusterControl(true)
	return []byte("+OK\r\n"), nil
}

func (s *Server) requireInternalClusterControl() error {
	// Compatibility mode: clusters that have not opted into a dedicated
	// control credential retain their previous behavior. Production cluster
	// deployments should configure cluster_control_auth.
	if s.clusterControlAuth == "" {
		return nil
	}

	// Direct in-process calls are trusted server-internal execution paths.
	// Network clients always have executionClient populated.
	if s.executionClient == nil {
		return nil
	}
	if !s.executionClient.internalClusterControlEnabled() {
		return errors.New("NOPERM internal cluster control authentication required")
	}
	return nil
}


func internalControlSecret(values []string) string {
	if len(values) == 0 {
		return ""
	}
	return values[0]
}

func authenticateInternalControlUpstream(
	conn net.Conn,
	reader *bufio.Reader,
	username, password, controlSecret string,
) error {
	if err := authenticateReplicationUpstream(conn, reader, username, password); err != nil {
		return err
	}
	if controlSecret == "" {
		return nil
	}
	if err := writeReplicationRESPCommand(conn, "SNUG.INTERNAL", "AUTH", controlSecret); err != nil {
		return err
	}
	line, err := readMigrateLine(reader)
	if err != nil {
		return err
	}
	if line == "+OK" {
		return nil
	}
	if strings.HasPrefix(line, "-") {
		return errors.New(strings.TrimPrefix(line, "-"))
	}
	return errors.New("invalid internal cluster control authentication response")
}

func failoverSubcommandRequiresInternalControl(subcommand string) bool {
	switch subcommand {
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
	default:
		return false
	}
}

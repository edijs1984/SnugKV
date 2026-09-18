package server

import "errors"

func cloneCommandArgs(args [][]byte) [][]byte {
	out := make([][]byte, len(args))
	for i := range args {
		out[i] = append([]byte(nil), args[i]...)
	}
	return out
}

func (s *Server) withExecutionACLContextLocked(
	session *authSession,
	args [][]byte,
	fn func() ([]byte, error),
) ([]byte, error) {
	previousUser := s.executionACLUsername
	previousArgs := s.executionACLArgs

	s.executionACLUsername = ""
	s.executionACLArgs = nil

	if session != nil && session.authenticated && session.username != "" {
		s.executionACLUsername = session.username
		s.executionACLArgs = cloneCommandArgs(args)
	}

	defer func() {
		s.executionACLUsername = previousUser
		s.executionACLArgs = previousArgs
	}()

	return fn()
}

func (s *Server) withNestedExecutionCommand(
	args [][]byte,
	fn func() ([]byte, error),
) ([]byte, error) {
	if s.executionACLUsername == "" {
		return fn()
	}

	previousArgs := s.executionACLArgs
	s.executionACLArgs = cloneCommandArgs(args)
	defer func() {
		s.executionACLArgs = previousArgs
	}()

	return fn()
}

func (s *Server) authorizeExecutionNestedCommand(args [][]byte) error {
	if s.executionACLUsername == "" {
		return nil
	}

	session := &authSession{
		username:      s.executionACLUsername,
		authenticated: true,
	}

	return s.authorizeConnectionCommand(session, args)
}

func (s *Server) authorizeExecutionDynamicKey(key string) error {
	if s.executionACLUsername == "" {
		return nil
	}

	user, ok := s.acl.GetUser(s.executionACLUsername)
	if !ok || !user.Enabled {
		return errors.New("NOAUTH Authentication required.")
	}

	if aclUserAllowsAdditionalKey(
		user,
		s.executionACLArgs,
		key,
	) {
		return nil
	}

	return errors.New("NOPERM No permissions to access a key")
}

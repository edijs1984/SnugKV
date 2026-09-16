package server

import "strings"

// routeStreamRename intercepts RENAME/RENAMENX only when the source is a
// STREAM. Returning nil without a handled stream leaves the existing routing
// chain responsible for other datatypes and missing-key errors.
func (s *Server) routeStreamRename(args [][]byte) error {
	if len(args) != 3 {
		return nil
	}
	cmd := strings.ToUpper(string(args[0]))
	if cmd != "RENAME" && cmd != "RENAMENX" {
		return nil
	}
	handled, renamed, err := s.store.RenameStream(string(args[1]), string(args[2]), cmd == "RENAMENX")
	if !handled {
		return nil
	}
	if err != nil {
		return err
	}
	// This helper cannot return a response, so handled successful renames are
	// routed directly by executePressureCommand in streamRenameResponse.
	_ = renamed
	return errStreamRenameHandled
}

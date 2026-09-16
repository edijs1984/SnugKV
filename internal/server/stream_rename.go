package server

import "strings"

// executeStreamRename intercepts RENAME/RENAMENX only when the source is a
// STREAM. handled=false leaves the existing datatype-aware rename chain
// responsible for all other source types and missing-key behavior.
func (s *Server) executeStreamRename(args [][]byte) ([]byte, bool, error) {
	if len(args) != 3 {
		return nil, false, nil
	}
	cmd := strings.ToUpper(string(args[0]))
	if cmd != "RENAME" && cmd != "RENAMENX" {
		return nil, false, nil
	}
	handled, renamed, err := s.store.RenameStream(string(args[1]), string(args[2]), cmd == "RENAMENX")
	if !handled {
		return nil, false, nil
	}
	if err != nil {
		return nil, true, err
	}
	if cmd == "RENAMENX" {
		return boolean(renamed), true, nil
	}
	return []byte("+OK\r\n"), true, nil
}

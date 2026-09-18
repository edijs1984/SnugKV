package server

import (
	"bufio"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

func aclUserFileLine(user *ACLUser) string {
	var out strings.Builder

	out.WriteString("user ")
	out.WriteString(user.Name)

	if user.Enabled {
		out.WriteString(" on")
	} else {
		out.WriteString(" off")
	}

	if user.NoPass {
		out.WriteString(" nopass")
	}

	out.WriteString(" sanitize-payload")

	for _, hash := range user.PasswordHashes {
		out.WriteString(" #")
		out.WriteString(hash)
	}

	for _, pattern := range user.KeyPatterns {
		out.WriteString(" ~")
		out.WriteString(pattern)
	}

	// SnugKV does not enforce channel ACLs yet. Redis ACL SAVE commonly
	// serializes unrestricted channel access as &*.
	out.WriteString(" &*")

	for _, rule := range user.CommandRules {
		out.WriteByte(' ')
		out.WriteString(rule)
	}

	return out.String()
}

func (a *ACL) SaveFile(path string) error {
	names := a.Users()
	sort.Strings(names)

	var contents strings.Builder

	for _, name := range names {
		user, ok := a.GetUser(name)
		if !ok {
			continue
		}

		contents.WriteString(aclUserFileLine(user))
		contents.WriteByte('\n')
	}

	dir := filepath.Dir(path)
	if dir == "" {
		dir = "."
	}

	base := filepath.Base(path)

	file, err := os.CreateTemp(
		dir,
		"."+base+".tmp-*",
	)
	if err != nil {
		return err
	}

	tempPath := file.Name()
	keep := false

	defer func() {
		_ = file.Close()

		if !keep {
			_ = os.Remove(tempPath)
		}
	}()

	if err := file.Chmod(0600); err != nil {
		return err
	}

	if _, err := file.WriteString(contents.String()); err != nil {
		return err
	}

	if err := file.Sync(); err != nil {
		return err
	}

	if err := file.Close(); err != nil {
		return err
	}

	if err := os.Rename(tempPath, path); err != nil {
		return err
	}

	keep = true

	return nil
}

func loadACLFile(path string) (*ACL, error) {
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()

	loaded := &ACL{
		users: make(map[string]*ACLUser),
	}

	scanner := bufio.NewScanner(file)

	// Keep ACL files bounded while allowing comfortably long rule lines.
	scanner.Buffer(
		make([]byte, 4096),
		1024*1024,
	)

	lineNumber := 0

	for scanner.Scan() {
		lineNumber++

		raw := strings.TrimSpace(scanner.Text())

		if raw == "" || strings.HasPrefix(raw, "# ") {
			continue
		}

		fields := strings.Fields(raw)

		if len(fields) < 2 ||
			!strings.EqualFold(fields[0], "user") {
			return nil, aclFileLoadError(
				path,
				lineNumber,
				"Syntax error",
			)
		}

		username := fields[1]

		if username == "" {
			return nil, aclFileLoadError(
				path,
				lineNumber,
				"Syntax error",
			)
		}

		rules := fields[2:]

		if err := loaded.SetUser(username, rules); err != nil {
			return nil, aclFileLoadError(
				path,
				lineNumber,
				aclFileErrorReason(err),
			)
		}
	}

	if err := scanner.Err(); err != nil {
		return nil, err
	}

	return loaded, nil
}

func aclFileErrorReason(err error) string {
	if err == nil {
		return "Syntax error"
	}

	message := err.Error()

	if strings.Contains(
		message,
		"Unknown command or category name in ACL",
	) {
		return "Unknown command or category name in ACL"
	}

	if strings.Contains(
		message,
		"Error in ACL SETUSER modifier",
	) {
		return "Syntax error"
	}

	return strings.TrimPrefix(message, "ERR ")
}

func aclFileLoadError(
	path string,
	line int,
	reason string,
) error {
	return fmt.Errorf(
		"ERR %s:%d: %s. WARNING: ACL errors detected, no change to the previously active ACL rules was performed",
		path,
		line,
		reason,
	)
}

func (a *ACL) LoadFile(path string) error {
	loaded, err := loadACLFile(path)
	if err != nil {
		return err
	}

	if loaded == nil {
		return errors.New("ERR failed to load ACL file")
	}

	a.ReplaceFrom(loaded)

	return nil
}

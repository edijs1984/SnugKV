package server

import "errors"

type luaClusterScope struct {
	allowCrossSlot bool
	runtimeSlot    int
}

func (s *Server) newLuaClusterScope(allowCrossSlot bool) *luaClusterScope {
	if !s.clusterEnabled {
		return nil
	}
	return &luaClusterScope{
		allowCrossSlot: allowCrossSlot,
		runtimeSlot:    -1,
	}
}

func (s *Server) validateLuaClusterAccess(scope *luaClusterScope, args [][]byte) error {
	if !s.clusterEnabled || scope == nil {
		return nil
	}

	refs, err := commandKeys(args)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}

	for _, ref := range refs {
		slot := clusterKeySlot(ref.value)
		owner := s.clusterSlotOwners[slot]
		if owner == "" {
			return errors.New("CLUSTERDOWN Hash slot not served")
		}
		if owner != s.clusterNodeAddr {
			return errors.New("ERR Script attempted to access a non local key in a cluster node")
		}

		if scope.allowCrossSlot {
			continue
		}
		if scope.runtimeSlot < 0 {
			scope.runtimeSlot = slot
			continue
		}
		if slot != scope.runtimeSlot {
			return errors.New("ERR Script attempted to access a non local key in a cluster node")
		}
	}

	return nil
}

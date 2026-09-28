package server

import (
	"errors"
	"sync"

	lua "github.com/yuin/gopher-lua"
)

type luaClusterScope struct {
	allowCrossSlot bool
	runtimeSlot    int
}

var luaClusterScopes sync.Map // map[*lua.LState]*luaClusterScope

func (s *Server) prepareLuaClusterScope(L *lua.LState, allowCrossSlot bool) func() {
	if !s.clusterEnabled {
		return func() {}
	}
	luaClusterScopes.Store(L, &luaClusterScope{
		allowCrossSlot: allowCrossSlot,
		runtimeSlot:    -1,
	})
	return func() {
		luaClusterScopes.Delete(L)
	}
}

func (s *Server) validateLuaClusterAccess(L *lua.LState, args [][]byte) error {
	if !s.clusterEnabled {
		return nil
	}

	refs, err := commandKeys(args)
	if err != nil {
		return err
	}
	if len(refs) == 0 {
		return nil
	}

	value, ok := luaClusterScopes.Load(L)
	if !ok {
		return nil
	}
	scope := value.(*luaClusterScope)

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

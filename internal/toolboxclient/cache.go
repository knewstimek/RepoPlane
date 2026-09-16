// Package toolboxclient provides the reference client-side contract cache for
// RepoPlane's toolbox.v1 surface. It deliberately tracks cached contracts and
// model-context residency as separate states.
package toolboxclient

import (
	"encoding/json"
	"errors"
	"strings"
	"sync"
)

var (
	ErrContractMissing   = errors.New("toolbox contract is missing")
	ErrRehydrateRequired = errors.New("toolbox contract must be rehydrated")
	ErrContractInvalid   = errors.New("toolbox contract is invalid")
)

type Key struct {
	ServerIdentity string
	Surface        string
	Toolbox        string
	Operation      string
}

type entry struct {
	handle   string
	contract json.RawMessage
	resident bool
}

type Cache struct {
	mu      sync.Mutex
	entries map[Key]entry
}

func New() *Cache { return &Cache{entries: make(map[Key]entry)} }

// Remember stores a server-described contract and marks it model-visible.
func (c *Cache) Remember(key Key, handle string, contract json.RawMessage) error {
	if !validKey(key) || !strings.HasPrefix(handle, "schema:"+key.Operation+"@sha256:") || len(contract) == 0 || !json.Valid(contract) {
		return ErrContractInvalid
	}
	var object map[string]any
	if json.Unmarshal(contract, &object) != nil || object == nil {
		return ErrContractInvalid
	}
	c.mu.Lock()
	defer c.mu.Unlock()
	c.entries[key] = entry{handle: handle, contract: append(json.RawMessage(nil), contract...), resident: true}
	return nil
}

// DescribeArguments asks for unchanged only while the contract is known to be
// resident in active model context. Otherwise it forces a full compact describe.
func (c *Cache) DescribeArguments(key Key) map[string]any {
	arguments := map[string]any{"action": "describe", "operation": key.Operation}
	c.mu.Lock()
	defer c.mu.Unlock()
	if current, ok := c.entries[key]; ok && current.resident {
		arguments["known_schema_handle"] = current.handle
	}
	return arguments
}

// CallArguments inserts the handle without asking the model to copy it. Calls
// are refused when compaction has made the contract non-resident.
func (c *Cache) CallArguments(key Key, arguments map[string]any) (map[string]any, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.entries[key]
	if !ok {
		return nil, ErrContractMissing
	}
	if !current.resident {
		return nil, ErrRehydrateRequired
	}
	return map[string]any{
		"action": "call", "operation": key.Operation,
		"schema_handle": current.handle, "arguments": arguments,
	}, nil
}

// Compact marks every cached contract non-resident without discarding bytes.
func (c *Cache) Compact() {
	c.mu.Lock()
	defer c.mu.Unlock()
	for key, current := range c.entries {
		current.resident = false
		c.entries[key] = current
	}
}

// Rehydrate returns cached contract bytes for reinjection and marks them
// resident. A caller may instead issue DescribeArguments, which omits the known
// handle while non-resident and asks the server to return the contract again.
func (c *Cache) Rehydrate(key Key) (json.RawMessage, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	current, ok := c.entries[key]
	if !ok {
		return nil, ErrContractMissing
	}
	current.resident = true
	c.entries[key] = current
	return append(json.RawMessage(nil), current.contract...), nil
}

func validKey(key Key) bool {
	return key.ServerIdentity != "" && key.Surface == "toolbox.v1" && key.Toolbox != "" && key.Operation != ""
}

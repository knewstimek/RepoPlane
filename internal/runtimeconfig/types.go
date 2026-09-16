// Package runtimeconfig defines the runtime service-configuration contract.
package runtimeconfig

import (
	"context"
	"errors"
	"strings"
)

const (
	ActionStatus  = "status"
	ActionAdd     = "add"
	ActionRemove  = "remove"
	ActionReplace = "replace"
	ActionSelect  = "select"
	ActionRefresh = "refresh"
	ActionStart   = "start"
	ActionStop    = "stop"

	TargetCatalogRoot   = "catalog_root"
	TargetCandidateRoot = "candidate_root"
	TargetRuleFile      = "rule_file"
	TargetSymbolIndex   = "symbol_index"
	TargetWorkspace     = "workspace"
	TargetStateDir      = "state_dir"
	TargetHTTPTransport = "http_transport"
	TargetToolSurface   = "tool_surface"
)

var (
	ErrInvalid     = errors.New("runtime configuration request is invalid")
	ErrUnavailable = errors.New("runtime configuration is unavailable for this transport")
	ErrBusy        = errors.New("runtime configuration conflicts with active work")
)

type Request struct {
	Action string   `json:"action" jsonschema:"status (read-only); add, remove, replace, refresh for source targets; select for workspace/state_dir; start or stop for http_transport"`
	Target string   `json:"target,omitempty" jsonschema:"catalog_root, candidate_root, rule_file, symbol_index, workspace, state_dir, or http_transport; omit for status"`
	Values []string `json:"values,omitempty" jsonschema:"one value for add/remove/select/start; array for replace; omit for status/refresh/stop"`
}

type Snapshot map[string][]string

type Response struct {
	Status        string   `json:"status"`
	Changed       bool     `json:"changed"`
	Refreshed     bool     `json:"refreshed"`
	Configuration Snapshot `json:"configuration"`
}

type Controller interface {
	Status(context.Context) (Response, error)
	Apply(context.Context, Request) (Response, error)
}

func Validate(request Request) error {
	request.Action = strings.TrimSpace(request.Action)
	request.Target = strings.TrimSpace(request.Target)
	if len(request.Values) > 64 {
		return ErrInvalid
	}
	for _, value := range request.Values {
		if len(value) > 4096 {
			return ErrInvalid
		}
	}
	if request.Action == ActionStatus {
		if request.Target != "" || len(request.Values) != 0 {
			return ErrInvalid
		}
		return nil
	}
	switch request.Target {
	case TargetCatalogRoot, TargetCandidateRoot, TargetRuleFile, TargetSymbolIndex:
		switch request.Action {
		case ActionAdd, ActionRemove:
			if len(request.Values) != 1 || strings.TrimSpace(request.Values[0]) == "" {
				return ErrInvalid
			}
		case ActionReplace:
			for _, value := range request.Values {
				if strings.TrimSpace(value) == "" {
					return ErrInvalid
				}
			}
		case ActionRefresh:
			if len(request.Values) != 0 {
				return ErrInvalid
			}
		default:
			return ErrInvalid
		}
	case TargetWorkspace, TargetStateDir:
		if request.Action != ActionSelect || len(request.Values) != 1 || strings.TrimSpace(request.Values[0]) == "" {
			return ErrInvalid
		}
	case TargetHTTPTransport:
		if request.Action == ActionStart {
			if len(request.Values) != 1 || strings.TrimSpace(request.Values[0]) == "" {
				return ErrInvalid
			}
		} else if request.Action == ActionStop {
			if len(request.Values) != 0 {
				return ErrInvalid
			}
		} else {
			return ErrInvalid
		}
	default:
		return ErrInvalid
	}
	return nil
}

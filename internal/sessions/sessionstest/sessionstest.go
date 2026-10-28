// Package sessionstest renders docker output in the exact formats session
// discovery asks for, for test fixtures (CS-SESS-118). Only tests import it.
package sessionstest

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Fields is the number of fields of one discovery row.
const Fields = 27

// Field indexes of a discovery row, in the format's order.
const (
	Name = iota
	Status
	Project
	Mode
	Instance
	Version
	Model
	ConfigHash
	Inputs
	PIDClass
	Worktree
	State
	CreatedAt
	MemoryLimit
	MemoryLimitSource
	retired
	ID
	ConfigDir
	Registry
	LaunchFlags
	Resume
	PeerRoot
	Terminal
	Continue
	Clipboard
	ShadowDir
	Mounts
)

// Row renders one docker ps line of the discovery format: fields in the
// format's order, each as a JSON string; fewer than Fields are padded with
// "" (so Mounts, the last, is empty unless given). More panics.
func Row(fields ...string) string {
	if len(fields) > Fields {
		panic(fmt.Sprintf("sessionstest.Row: %d fields, the format has %d", len(fields), Fields))
	}
	full := make([]string, Fields)
	copy(full, fields)
	return Raw(full...)
}

// Raw JSON-encodes and joins exactly the fields given, whatever their count:
// for a test of a row that does not match the format.
func Raw(fields ...string) string {
	out := make([]string, len(fields))
	for i, f := range fields {
		b, err := json.Marshal(f)
		if err != nil {
			panic(err)
		}
		out[i] = string(b)
	}
	return strings.Join(out, "\x1f")
}

// Set returns a full row's fields with the given indexes set.
func Set(base []string, kv map[int]string) []string {
	full := make([]string, Fields)
	copy(full, base)
	for k, v := range kv {
		full[k] = v
	}
	return full
}

// InspectRow renders one line of InspectContainer's format (CS-TMUX-051).
func InspectRow(name, state, created, project, instance, mode string) string {
	return Raw(name, state, created, project, instance, mode)
}

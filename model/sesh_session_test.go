package model

import (
	"bytes"
	"encoding/gob"
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestPaneIdentityRemainsInternalAndCacheable(t *testing.T) {
	selected := SeshSession{
		Src: "tmux-pane", Name: "code/editor", Path: "/original",
		TmuxSessionID: "$1", TmuxWindowID: "@3", TmuxPaneID: "%7", TmuxServerGeneration: "101:1700000000:/tmp/tmux-fixture",
	}
	data, err := json.Marshal(selected)
	require.NoError(t, err)
	var public map[string]any
	require.NoError(t, json.Unmarshal(data, &public))
	require.Equal(t, selected.Name, public["Name"])
	require.Equal(t, selected.Path, public["Path"])
	for _, field := range []string{"TmuxSessionID", "TmuxWindowID", "TmuxPaneID", "TmuxServerGeneration"} {
		require.NotContains(t, public, field)
	}
	var cached bytes.Buffer
	require.NoError(t, gob.NewEncoder(&cached).Encode(selected))
	var restored SeshSession
	require.NoError(t, gob.NewDecoder(&cached).Decode(&restored))
	require.Equal(t, selected, restored)
}

// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package ui

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

func TestAvailableNode_JSONRoundTripPreservesBrokerIdentityFields(t *testing.T) {
	const body = `{"hostUuid":"host-a","discoveryRecordId":"observation:candidate-a","identityVerification":"unverified"}`
	var node availableNode
	require.NoError(t, json.Unmarshal([]byte(body), &node))
	wire, err := json.Marshal(node)
	require.NoError(t, err)
	type nodeIdentityFields struct {
		DiscoveryRecordID    string `json:"discoveryRecordId"`
		IdentityVerification string `json:"identityVerification"`
	}
	var got nodeIdentityFields
	require.NoError(t, json.Unmarshal(wire, &got))
	assert.Equal(t, "observation:candidate-a", got.DiscoveryRecordID)
	assert.Equal(t, "unverified", got.IdentityVerification)
}

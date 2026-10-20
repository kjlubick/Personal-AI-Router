// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
	"nvpair-shared/noderec"
)

// A candidate copying a verified UUID must still be selectable for pairing by
// its discovery record ID. UUID lookup must keep returning the verified peer's
// endpoint, regardless of which record arrives first.
func TestBrowser_ResolvesDiscoveryRecordsWithoutReplacingVerifiedHost(t *testing.T) {
	const peerUUID = "peer"
	const peerAddress = "192.0.2.20"
	const candidateRecordID = "candidate:rogue"
	const candidateAddress = "192.0.2.30"
	const clusterPort = 14321
	peer := noderec.DirectoryNode{
		HostUUID:             peerUUID,
		DiscoveryRecordID:    "peer:peer",
		IdentityVerification: noderec.IdentityPinned,
		IP:                   peerAddress,
		Services: map[noderec.ServiceKey]noderec.ServiceStatus{
			noderec.ServiceCluster: {Port: clusterPort},
		},
	}
	candidate := peer
	candidate.DiscoveryRecordID = candidateRecordID
	candidate.IdentityVerification = noderec.IdentityUnverified
	candidate.IP = candidateAddress
	test := func(name string, records []noderec.DirectoryNode) {
		t.Run(name, func(t *testing.T) {
			browser := newBrowser()
			browser.setRelay(records)

			addresses, port, ok := browser.Resolve(candidateRecordID)
			require.True(t, ok)
			assert.Equal(t, []string{candidateAddress}, addresses)
			assert.Equal(t, clusterPort, port)
			addresses, _, ok = browser.Resolve(peerUUID)
			require.True(t, ok)
			assert.Equal(t, []string{peerAddress}, addresses)
		})
	}
	test("peer arrives first", []noderec.DirectoryNode{peer, candidate})
	test("candidate arrives first", []noderec.DirectoryNode{candidate, peer})
}

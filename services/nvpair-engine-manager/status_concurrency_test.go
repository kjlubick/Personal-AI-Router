// SPDX-FileCopyrightText: Copyright (c) 2026 NVIDIA CORPORATION & AFFILIATES. All rights reserved.
// SPDX-License-Identifier: Apache-2.0

package main

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"runtime"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func blockedPresenceExecutor(t *testing.T) (*Executor, int, <-chan struct{}, func(), *atomic.Int32) {
	t.Helper()
	started := make(chan struct{}, 1)
	release := make(chan struct{})
	var once sync.Once
	unblock := func() { once.Do(func() { close(release) }) }
	requests := &atomic.Int32{}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/" {
			http.NotFound(w, r)
			return
		}
		requests.Add(1)
		select {
		case started <- struct{}{}:
		default:
		}
		select {
		case <-release:
			w.WriteHeader(http.StatusOK)
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		unblock()
		srv.Close()
	})
	port := portOf(t, srv.URL)
	m := testEngineManifest(filepath.Join(t.TempDir(), "missing"+exeExt()))
	platform := m.Platforms[runtime.GOOS+"/"+runtime.GOARCH]
	platform.Runtime.Port = port
	platform.Runtime.Health = nil
	m.Platforms[runtime.GOOS+"/"+runtime.GOARCH] = platform
	return newTestExecutor(t, m), port, started, unblock, requests
}

func awaitStatusTestResult[T any](t *testing.T, ch <-chan T) T {
	t.Helper()
	select {
	case result := <-ch:
		return result
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for status operation")
		var zero T
		return zero
	}
}

func TestStatusReads_ReturnWithoutEngineOperationLock(t *testing.T) {
	test := func(name string, read func(*Executor) (EngineStatus, error)) {
		t.Run(name, func(t *testing.T) {
			ex := newTestExecutor(t, testEngineManifest(filepath.Join(t.TempDir(), "missing"+exeExt())))
			st, err := ex.state("fake")
			require.NoError(t, err)
			st.lockOperation()
			defer st.unlockOperation()

			type result struct {
				status EngineStatus
				err    error
			}
			finished := make(chan result, 1)
			go func() {
				status, err := read(ex)
				finished <- result{status, err}
			}()
			select {
			case got := <-finished:
				require.NoError(t, got.err)
				require.False(t, got.status.Running)
			case <-time.After(time.Second):
				require.FailNow(t, "status read waited for the lifecycle operation lock")
			}
		})
	}
	test("status", func(ex *Executor) (EngineStatus, error) { return ex.Status("fake") })
	test("get installed", func(ex *Executor) (EngineStatus, error) {
		statuses := ex.GetInstalled()
		if len(statuses) != 1 {
			return EngineStatus{}, fmt.Errorf("expected one engine, got %d", len(statuses))
		}
		return statuses[0], nil
	})
}

func TestStatus_DoesNotReadoptAfterStopDuringProbe(t *testing.T) {
	ex, _, started, unblock, _ := blockedPresenceExecutor(t)
	type result struct {
		status EngineStatus
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		status, err := ex.Status("fake")
		finished <- result{status, err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "readiness probe did not start")
	}

	stopped := make(chan error, 1)
	go func() { stopped <- ex.Stop("fake") }()
	select {
	case err := <-stopped:
		require.NoError(t, err)
	case <-time.After(time.Second):
		unblock()
		require.FailNow(t, "Stop waited for a passive readiness probe")
	}
	unblock()
	got := awaitStatusTestResult(t, finished)
	require.NoError(t, got.err)
	require.False(t, got.status.Running, "an old probe re-adopted after Stop")
	status, err := ex.Status("fake")
	require.NoError(t, err)
	require.False(t, status.Running, "later status undid an explicit Stop")
}

func TestStatus_DoesNotRestoreOldPortAfterSetPort(t *testing.T) {
	ex, oldPort, started, unblock, _ := blockedPresenceExecutor(t)
	ex.overrideDir = t.TempDir()
	newPort, err := freePort()
	require.NoError(t, err)
	for newPort == oldPort {
		newPort, err = freePort()
		require.NoError(t, err)
	}
	type result struct {
		status EngineStatus
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		status, err := ex.Status("fake")
		finished <- result{status, err}
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "readiness probe did not start")
	}

	changed := make(chan result, 1)
	go func() {
		status, err := ex.SetPort(context.Background(), "fake", newPort)
		changed <- result{status, err}
	}()
	select {
	case got := <-changed:
		require.NoError(t, got.err)
		require.Equal(t, newPort, got.status.Port)
	case <-time.After(time.Second):
		unblock()
		require.FailNow(t, "SetPort waited for a passive readiness probe")
	}
	unblock()
	got := awaitStatusTestResult(t, finished)
	require.NoError(t, got.err)
	require.Equal(t, newPort, got.status.Port, "an old probe restored the former port")
	status, err := ex.Status("fake")
	require.NoError(t, err)
	require.Equal(t, newPort, status.Port)
}

func TestStatus_SecondReaderUsesProbeAfterFirstCancels(t *testing.T) {
	ex, _, started, unblock, requests := blockedPresenceExecutor(t)
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	first := make(chan error, 1)
	go func() {
		_, err := ex.statusWithContext(ctx, "fake", 0, false)
		first <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "readiness probe did not start")
	}
	type result struct {
		status EngineStatus
		err    error
	}
	second := make(chan result, 1)
	go func() {
		status, err := ex.Status("fake")
		second <- result{status, err}
	}()
	state, err := ex.state("fake")
	require.NoError(t, err)
	joined := false
	deadline := time.Now().Add(2 * time.Second)
	for time.Now().Before(deadline) {
		state.mu.Lock()
		for _, run := range state.statusRuns {
			joined = run.waiters == 2
		}
		state.mu.Unlock()
		if joined {
			break
		}
		time.Sleep(time.Millisecond)
	}
	require.True(t, joined, "second reader did not join the shared probe")
	cancel()
	require.ErrorIs(t, awaitStatusTestResult(t, first), context.Canceled)
	unblock()
	got := awaitStatusTestResult(t, second)
	require.NoError(t, got.err)
	require.True(t, got.status.Running)
	require.Equal(t, int32(1), requests.Load(), "readers started duplicate readiness requests")
}

func TestStatusAtPort_ProbesAfterActiveOperation(t *testing.T) {
	ex, port, started, unblock, _ := blockedPresenceExecutor(t)
	st, err := ex.state("fake")
	require.NoError(t, err)
	st.lockOperation()
	var once sync.Once
	unlock := func() { once.Do(st.unlockOperation) }
	defer unlock()
	type result struct {
		status EngineStatus
		err    error
	}
	finished := make(chan result, 1)
	go func() {
		status, err := ex.StatusAtPort("fake", port)
		finished <- result{status, err}
	}()
	select {
	case <-started:
		require.FailNow(t, "one-shot port probe ran during an active operation")
	case <-time.After(100 * time.Millisecond):
	}
	unlock()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "one-shot port probe did not run after the operation")
	}
	unblock()
	got := awaitStatusTestResult(t, finished)
	require.NoError(t, got.err)
	require.True(t, got.status.Running)
	require.Equal(t, port, got.status.Port)
}

func TestModelsHandler_RejectsSweepThatExpiresDuringStatusProbe(t *testing.T) {
	ex, _, started, unblock, _ := blockedPresenceExecutor(t)
	m, ok := ex.reg.Get("fake")
	require.True(t, ok)
	m.Actions["list_models"] = Action{
		HTTP:   &ActionHTTP{Method: http.MethodGet, Path: "/api/tags"},
		Result: &ActionResult{Array: "models", Field: "name"},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, modelsPath, nil).WithContext(ctx)
	response := httptest.NewRecorder()
	finished := make(chan struct{})
	go func() {
		modelsHandler(ex)(response, req)
		close(finished)
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "readiness probe did not start")
	}
	select {
	case <-finished:
	case <-time.After(time.Second):
		unblock()
		require.FailNow(t, "inventory response ignored its deadline")
	}
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
	require.NotContains(t, response.Body.String(), `"models":[]`)
	unblock()
}

func TestModelsHandler_RejectsSweepThatExpiresDuringEngineOperation(t *testing.T) {
	ex := newTestExecutor(t, testEngineManifest(filepath.Join(t.TempDir(), "missing"+exeExt())))
	m, ok := ex.reg.Get("fake")
	require.True(t, ok)
	m.Actions["list_models"] = Action{
		HTTP:   &ActionHTTP{Method: http.MethodGet, Path: "/api/tags"},
		Result: &ActionResult{Array: "models", Field: "name"},
	}
	st, err := ex.state("fake")
	require.NoError(t, err)
	st.lockOperation()
	defer st.unlockOperation()
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	req := httptest.NewRequest(http.MethodGet, modelsPath, nil).WithContext(ctx)
	response := httptest.NewRecorder()
	modelsHandler(ex)(response, req)
	require.Equal(t, http.StatusServiceUnavailable, response.Code)
}

func TestModelsResult_CanceledCallerReturnsBeforeProbeCompletes(t *testing.T) {
	ex, _, started, unblock, _ := blockedPresenceExecutor(t)
	m, ok := ex.reg.Get("fake")
	require.True(t, ok)
	m.Actions["list_models"] = Action{
		HTTP:   &ActionHTTP{Method: http.MethodGet, Path: "/api/tags"},
		Result: &ActionResult{Array: "models", Field: "name"},
	}
	ctx, cancel := context.WithCancel(context.Background())
	finished := make(chan error, 1)
	go func() {
		_, err := ex.modelsResult(ctx)
		finished <- err
	}()
	select {
	case <-started:
	case <-time.After(2 * time.Second):
		require.FailNow(t, "readiness probe did not start")
	}
	cancel()
	require.ErrorIs(t, awaitStatusTestResult(t, finished), context.Canceled)
	unblock()
	status, err := ex.Status("fake")
	require.NoError(t, err)
	require.True(t, status.Running)
}

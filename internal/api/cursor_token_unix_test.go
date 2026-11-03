//go:build !windows

package api

import (
	"errors"
	"testing"
)

func TestWriteCursorCredentials_FailsWhenRefreshTokenIsNotPersisted(t *testing.T) {
	SetCursorTestMode(false)

	origSQLite := cursorWriteSQLiteToken
	origKeychain := cursorWriteKeychain
	origKeyring := cursorWriteLinuxKeyring
	cursorWriteSQLiteToken = func(dbPath, key, value string) error {
		if key == "cursorAuth/refreshToken" {
			return errors.New("refresh write failed")
		}
		return nil
	}
	cursorWriteKeychain = func(service, value string) error {
		if service == "cursor-refresh-token" {
			return errors.New("refresh write failed")
		}
		return nil
	}
	cursorWriteLinuxKeyring = func(service, value string) error {
		if service == "cursor-refresh-token" {
			return errors.New("refresh write failed")
		}
		return nil
	}
	t.Cleanup(func() {
		cursorWriteSQLiteToken = origSQLite
		cursorWriteKeychain = origKeychain
		cursorWriteLinuxKeyring = origKeyring
	})

	err := writeCursorCredentials("fresh_access", "fresh_refresh")
	if err == nil {
		t.Fatal("expected writeCursorCredentials to fail when refresh token persistence fails")
	}
}

func TestReadCursorKeychainValue_FallsBackToCursorAgentAccount(t *testing.T) {
	origRead := cursorReadKeychainPassword
	var calls []string
	cursorReadKeychainPassword = func(service, account string) (string, error) {
		calls = append(calls, service+":"+account)
		if account == "cursor-user" {
			return "cursor_agent_token", nil
		}
		return "", errors.New("not found")
	}
	t.Cleanup(func() {
		cursorReadKeychainPassword = origRead
	})

	got := readCursorKeychainValue("cursor-access-token", "rob")
	if got != "cursor_agent_token" {
		t.Fatalf("readCursorKeychainValue() = %q, want Cursor Agent token", got)
	}

	wantCalls := []string{
		"cursor-access-token:rob",
		"cursor-access-token:cursor-user",
	}
	if len(calls) != len(wantCalls) {
		t.Fatalf("keychain lookup calls = %v, want %v", calls, wantCalls)
	}
	for i := range wantCalls {
		if calls[i] != wantCalls[i] {
			t.Errorf("keychain lookup call %d = %q, want %q", i, calls[i], wantCalls[i])
		}
	}
}

package mockgithub

import (
	"crypto/rand"
	"encoding/hex"
	"strings"
)

type token struct {
	installationID int64
	expires        int64 // unix seconds
}

// state is everything the mock remembers. Guarded by Server.mu.
type state struct {
	installations map[int64]Installation
	repos         map[string]Repo // key: repoKey
	pulls         map[string][]PR
	checkRuns     []CheckRun
	nextCheckID   int64
	instTokens    map[string]token
	users         map[string]*User  // by login
	userTokens    map[string]string // access token -> login
	codes         map[string]string // oauth code -> login
	defaultUser   string
	calls         []*Call
	callSeq       int
	faults        []*Fault
	slack         []SlackPost
}

func newState() *state {
	return &state{
		installations: map[int64]Installation{},
		repos:         map[string]Repo{},
		pulls:         map[string][]PR{},
		nextCheckID:   1000,
		instTokens:    map[string]token{},
		users:         map[string]*User{},
		userTokens:    map[string]string{},
		codes:         map[string]string{},
	}
}

func repoKey(owner, name string) string {
	return strings.ToLower(owner) + "/" + strings.ToLower(name)
}

func randomHex(n int) string {
	b := make([]byte, n)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)
}

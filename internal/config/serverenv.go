package config

import (
	"os"
	"sync"

	"github.com/qoryai/forager/accesskey"
)

// ServerVariables are the variables qory takes for the server from its environment: the
// access key's id, its secret and the pin, QORY_ACCESS_KEY_ID, QORY_ACCESS_KEY_SECRET
// and QORY_APIARY_PUBLIC_KEY, and QORY_SERVER_SECRET, which held a workspace access
// key's secret and is only refused. An unset variable is empty.
type ServerVariables struct {
	AccessKeyID     string
	AccessKeySecret string
	ApiaryPublicKey string
	WorkspaceSecret string
}

// envWorkspaceSecret is the variable that held a workspace access key's secret, before
// a machine signed with an access key of its own. It is refused, as gateway.server.access_key
// and gateway.server.secret are.
const envWorkspaceSecret = "QORY_SERVER_SECRET"

// serverVariableNames are the names of [ServerVariables].
var serverVariableNames = []string{accesskey.EnvID, accesskey.EnvSecret, accesskey.EnvPin, envWorkspaceSecret}

var (
	takenMu sync.Mutex
	taken   ServerVariables
)

// TakeServerVariables reads the four [ServerVariables] from the process's environment
// into memory, replacing what an earlier call took, and removes them from the
// environment, so no program qory starts inherits them. qory calls it once, when a
// command starts and before it starts anything; everything that needs a value reads it
// with [TakenServerVariables].
func TakeServerVariables() {
	v := ServerVariables{
		AccessKeyID:     os.Getenv(accesskey.EnvID),
		AccessKeySecret: os.Getenv(accesskey.EnvSecret),
		ApiaryPublicKey: os.Getenv(accesskey.EnvPin),
		WorkspaceSecret: os.Getenv(envWorkspaceSecret),
	}
	for _, name := range serverVariableNames {
		os.Unsetenv(name)
	}
	SetServerVariables(v)
}

// TakenServerVariables is what [TakeServerVariables] took, or what a test set with
// [SetServerVariables].
func TakenServerVariables() ServerVariables {
	takenMu.Lock()
	defer takenMu.Unlock()
	return taken
}

// SetServerVariables replaces what [TakeServerVariables] took, so a test reads
// forager.yaml with the values it chooses.
func SetServerVariables(v ServerVariables) {
	takenMu.Lock()
	defer takenMu.Unlock()
	taken = v
}

package session

import (
	"os"
	"testing"

	"github.com/whyrusleeping/ycc/internal/jobshim"
)

func TestMain(m *testing.M) {
	jobshim.MaybeHelper() // supervised background Bash re-execs the test binary
	os.Exit(m.Run())
}

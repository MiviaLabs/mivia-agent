package cliorchestrate

import (
	"context"
	"fmt"
	"io"

	"github.com/MiviaLabs/mivia-agent/internal/chatsync"
	cliagents "github.com/MiviaLabs/mivia-agent/internal/cliagents"
	"github.com/MiviaLabs/mivia-agent/internal/config"
)

// syncProbe is the reachability seam. Production probes the network; tests
// substitute a closure so `mivia doctor` never leaves the test process.
var syncProbe = chatsync.ProbeEndpoint

// doctorSyncReport is what doctor says about chat sync: where it would
// upload, what supplied that answer, whether it could log in, and whether one
// bounded request reached the host.
type doctorSyncReport struct {
	Disabled bool
	Endpoint chatsync.Endpoint
	// LoginPresent is the exact activation predicate the hosts use
	// (config.ResolvedSync.Active with DefaultTokenProvider): sync runs only
	// when a token provider can be built from the saved login.
	LoginPresent bool
	Probe        string
}

// doctorSync resolves the sync endpoint through the same resolver OpenSession
// uses, so the answer doctor prints is the answer sync acts on. The probe runs
// only with a login present: sync activates only then, and probing a host the
// CLI could never authenticate to would report a state that cannot occur.
func doctorSync(res *config.Resolved) doctorSyncReport {
	if res.Sync.Disabled {
		return doctorSyncReport{Disabled: true}
	}
	r := doctorSyncReport{
		Endpoint:     chatsync.ResolveEndpoint(res.Sync.APIURL),
		LoginPresent: chatsync.DefaultTokenProvider() != nil,
	}
	if !r.LoginPresent {
		r.Probe = "skipped (not logged in)"
		return r
	}
	_, r.Probe = syncProbe(context.Background(), r.Endpoint.URL)
	return r
}

func (r doctorSyncReport) login() string {
	if r.LoginPresent {
		return "present"
	}
	return "absent (run mivia login)"
}

func writeDoctorSyncHuman(stdout io.Writer, r doctorSyncReport) {
	if r.Disabled {
		fmt.Fprintln(stdout, "  sync_api:   disabled ([sync] enabled = false)")
		return
	}
	fmt.Fprintf(stdout, "  sync_api:   %s (%s)\n", safeDoctorURL(r.Endpoint.URL), cliagents.SafeCatalogText(r.Endpoint.Source, 240))
	fmt.Fprintf(stdout, "  sync_login: %s\n", r.login())
	fmt.Fprintf(stdout, "  sync_probe: %s\n", cliagents.SafeCatalogText(r.Probe, 240))
}

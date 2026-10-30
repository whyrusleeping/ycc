package health

import (
	"context"
	"errors"
	"fmt"
	"log"
	"net"
	"path/filepath"
	"time"

	"github.com/whyrusleeping/ycc/internal/config"
	"github.com/whyrusleeping/ycc/internal/engine"
	"github.com/whyrusleeping/ycc/internal/tools"
)

const (
	modelInterval = time.Hour
	exaInterval   = 6 * time.Hour
	diskInterval  = 5 * time.Minute

	diskWarnBytes = 20 << 30
	diskFailBytes = 10 << 30

	// ExaID is the check id of the Exa web API.
	ExaID = "exa"
)

// ModelID is the check id of a logical model.
func ModelID(name string) string { return "model:" + name }

// Sources supplies the daemon state the standard checks are derived from.
type Sources struct {
	Registry *config.Registry
	// DiskPaths lists the directories whose filesystems must keep free space
	// (daemon state, registered projects); one check is made per filesystem.
	DiskPaths func() []string
}

// Targets returns the standard check set: probed models, Exa, and disk.
func Targets(src Sources) func() []Target {
	return func() []Target {
		var out []Target
		for _, name := range src.Registry.HealthProbeModels() {
			out = append(out, modelTarget(src.Registry, name))
		}
		out = append(out, Target{ID: ExaID, Label: "Web search (Exa)", Interval: exaInterval, Run: runExa})
		if src.DiskPaths != nil {
			out = append(out, diskTargets(src.DiskPaths())...)
		}
		return out
	}
}

// Install wires real model turns and web tool requests into m.
func Install(m *Monitor) {
	engine.SetTurnObserver(func(model string, err error) {
		if err != nil && engine.ClassifyAPIError(err).Kind == engine.KindContextLength {
			return // specific to that conversation, not the provider
		}
		m.Observe(ModelID(model), err)
	})
	tools.SetExaObserver(func(err error) { m.Observe(ExaID, err) })
}

func modelTarget(reg *config.Registry, name string) Target {
	return Target{
		ID: ModelID(name), Label: "Model " + name, Interval: modelInterval,
		Run: func(ctx context.Context) Check {
			m, ok := reg.GetModel(name)
			if !ok {
				return Check{Status: StatusPending}
			}
			d, err := config.ProbeModel(ctx, name, m)
			if err == nil {
				return Check{Status: StatusOK, Detail: fmt.Sprintf("Responded in %.1fs.", d.Seconds())}
			}
			log.Printf("health: model %s probe failed: %v", name, err)
			return modelFailure(m, err)
		},
	}
}

// modelFailure describes a failed probe categorically: provider error bodies
// can echo request content, so only the classification reaches clients.
func modelFailure(m config.Model, err error) Check {
	kind := engine.KindTimeout
	var info engine.APIErrorInfo
	if !errors.Is(err, context.DeadlineExceeded) {
		info = engine.ClassifyAPIError(err)
		kind = info.Kind
	}
	status := ""
	if info.Status != 0 {
		status = fmt.Sprintf(" (HTTP %d)", info.Status)
	}
	login := "check the login or API key"
	switch {
	case m.Auth == "oauth":
		login = fmt.Sprintf("run `ycc login %s` on the daemon host", m.Backend)
	case m.KeyEnv != "":
		login = fmt.Sprintf("check %s (`ycc token set %s` on the daemon host)", m.KeyEnv, m.KeyEnv)
	}
	switch kind {
	case engine.KindAuth:
		return Check{Status: StatusFail, Detail: "Authentication failed" + status + ".", Remedy: "Re-authenticate: " + login + "."}
	case engine.KindRateLimit:
		return Check{Status: StatusWarn, Detail: "Rate limited or out of quota" + status + ".", Remedy: "Check the provider plan, usage limits, or billing."}
	case engine.KindOverloaded, engine.KindServer:
		return Check{Status: StatusWarn, Detail: "Provider temporarily unavailable" + status + ".", Remedy: "Usually transient; check the provider status page if it persists."}
	case engine.KindTimeout:
		return Check{Status: StatusWarn, Detail: "The test request timed out."}
	case engine.KindNetwork:
		return Check{Status: StatusFail, Detail: "Could not reach the provider endpoint.", Remedy: "Check the daemon host's network and the model's base URL."}
	case engine.KindInvalidRequest, engine.KindContextLength:
		return Check{Status: StatusFail, Detail: "The provider rejected the request" + status + ".", Remedy: "Check the model id, backend, base URL, and reasoning settings; billing problems also show up this way."}
	default:
		return Check{Status: StatusFail, Detail: "The test request failed" + status + ".", Remedy: "Check the daemon log; " + login + "."}
	}
}

func runExa(ctx context.Context) Check {
	configured, status, err := tools.ProbeExa(ctx)
	switch {
	case !configured:
		return Check{Status: StatusOff, Detail: "EXA_API_KEY is not set; web_search and fetch_page are disabled.", Remedy: "Run `ycc token set EXA_API_KEY` on the daemon host to enable them."}
	case err == nil:
		return Check{Status: StatusOK, Detail: "Exa API responded."}
	}
	log.Printf("health: exa probe failed: %v", err)
	switch {
	case status == 401 || status == 403:
		return Check{Status: StatusFail, Detail: fmt.Sprintf("Exa rejected the API key (HTTP %d).", status), Remedy: "Check EXA_API_KEY (`ycc token set EXA_API_KEY` on the daemon host)."}
	case status == 402:
		return Check{Status: StatusFail, Detail: "Exa reports a billing problem (HTTP 402).", Remedy: "Add credits or a payment method in the Exa dashboard."}
	case status == 429:
		return Check{Status: StatusWarn, Detail: "Exa rate limit or quota reached (HTTP 429).", Remedy: "Check usage limits in the Exa dashboard."}
	case status != 0:
		return Check{Status: StatusFail, Detail: fmt.Sprintf("Exa API returned HTTP %d.", status)}
	case isTimeout(err):
		return Check{Status: StatusWarn, Detail: "The Exa request timed out."}
	default:
		return Check{Status: StatusFail, Detail: "Could not reach the Exa API.", Remedy: "Check the daemon host's network."}
	}
}

func isTimeout(err error) bool {
	var ne net.Error
	return errors.Is(err, context.DeadlineExceeded) || (errors.As(err, &ne) && ne.Timeout())
}

// diskTargets returns one check per distinct filesystem under paths.
func diskTargets(paths []string) []Target {
	seen := map[uint64]bool{}
	var out []Target
	for _, p := range paths {
		dir := existingDir(p)
		if dir == "" {
			continue
		}
		dev, ok := deviceID(dir)
		if !ok || seen[dev] {
			continue
		}
		seen[dev] = true
		dir = mountPoint(dir, dev)
		out = append(out, Target{
			ID: "disk:" + dir, Label: "Disk space (" + dir + ")", Interval: diskInterval,
			Run: func(context.Context) Check { return diskCheck(dir) },
		})
	}
	return out
}

func diskCheck(dir string) Check {
	free, err := freeBytes(dir)
	if err != nil {
		return Check{Status: StatusWarn, Detail: "Could not read free space: " + err.Error()}
	}
	detail := fmt.Sprintf("%s free.", formatBytes(free))
	switch {
	case free < diskFailBytes:
		return Check{Status: StatusFail, Detail: detail, Remedy: "Free space now: sessions, worktrees, and builds fail when the disk fills."}
	case free < diskWarnBytes:
		return Check{Status: StatusWarn, Detail: detail, Remedy: "Free some space soon."}
	}
	return Check{Status: StatusOK, Detail: detail}
}

// existingDir returns the nearest existing ancestor of p, so a state
// directory that does not exist yet still maps to its filesystem.
func existingDir(p string) string {
	p = filepath.Clean(p)
	for {
		if isDir(p) {
			return p
		}
		parent := filepath.Dir(p)
		if parent == p {
			return ""
		}
		p = parent
	}
}

// mountPoint returns the topmost ancestor of dir on the same filesystem, which
// names the filesystem better than whichever path happened to be listed first.
func mountPoint(dir string, dev uint64) string {
	for {
		parent := filepath.Dir(dir)
		if parent == dir {
			return dir
		}
		if d, ok := deviceID(parent); !ok || d != dev {
			return dir
		}
		dir = parent
	}
}

func formatBytes(b uint64) string {
	const gb = 1 << 30
	if b >= 10*gb {
		return fmt.Sprintf("%d GB", b/gb)
	}
	return fmt.Sprintf("%.1f GB", float64(b)/gb)
}

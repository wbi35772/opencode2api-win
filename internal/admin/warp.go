package admin

import (
	"errors"
	"net/http"
	"time"

	"opencode2api/internal/httpx"
	"opencode2api/internal/warp"
)

// defaultManualBanHours is the ban length used when the operator only submits a
// subnet. A day matches the quota cooldown the pool applies on its own, so a
// manual ban is never weaker than an automatic one unless asked to be.
const defaultManualBanHours = 24

// maxManualBanHours bounds a hand-written ban so a typo cannot blacklist a
// block for years.
const maxManualBanHours = 24 * 30

type warpBanInput struct {
	Subnet string  `json:"subnet"`
	Hours  float64 `json:"hours"`
}

type warpReleaseInput struct {
	Subnet string `json:"subnet"`
}

// handleWarpBlacklist lists every public egress block the pool refuses to
// register, with the expiry, the source of the ban and how many live exits
// still serve it.
func (a *Server) handleWarpBlacklist(w http.ResponseWriter, r *http.Request) {
	pool := a.manager.WarpPool()
	if pool == nil {
		writeAdminError(w, http.StatusConflict, "warp_disabled", "WARP is not enabled in the running configuration")
		return
	}
	entries := pool.Blacklist()
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"entries":    entries,
		"count":      len(entries),
		"state_path": pool.StatePath(),
		"now":        time.Now().UTC(),
	})
}

// handleWarpBan adds a manual ban. It accepts a bare address or the exact block
// (/32 or /64); the pool retires idle exits on it immediately and persists the
// entry beside the automatic quota penalties.
func (a *Server) handleWarpBan(w http.ResponseWriter, r *http.Request) {
	pool := a.manager.WarpPool()
	if pool == nil {
		writeAdminError(w, http.StatusConflict, "warp_disabled", "WARP is not enabled in the running configuration")
		return
	}
	var input warpBanInput
	if err := decodeAdminJSON(w, r, &input); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	hours := input.Hours
	if hours <= 0 {
		hours = defaultManualBanHours
	}
	if hours > maxManualBanHours {
		hours = maxManualBanHours
	}
	until := time.Now().Add(time.Duration(hours * float64(time.Hour)))
	entry, err := pool.BanEgress(input.Subnet, until)
	if err != nil {
		code := "ban_failed"
		if errorsIsInvalidBlock(err) {
			code = "invalid_subnet"
		}
		writeAdminError(w, http.StatusBadRequest, code, a.manager.Redact(err.Error()))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"entry": entry})
}

// handleWarpRelease lifts a ban, whether it was added by hand or by an upstream
// quota rejection.
func (a *Server) handleWarpRelease(w http.ResponseWriter, r *http.Request) {
	pool := a.manager.WarpPool()
	if pool == nil {
		writeAdminError(w, http.StatusConflict, "warp_disabled", "WARP is not enabled in the running configuration")
		return
	}
	var input warpReleaseInput
	if err := decodeAdminJSON(w, r, &input); err != nil {
		writeAdminError(w, http.StatusBadRequest, "invalid_request", err.Error())
		return
	}
	released, err := pool.ReleaseEgress(input.Subnet)
	if err != nil {
		code := "release_failed"
		if errorsIsInvalidBlock(err) {
			code = "invalid_subnet"
		}
		writeAdminError(w, http.StatusBadRequest, code, a.manager.Redact(err.Error()))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{"released": released})
}

// handleWarpReloadHot applies a hand-edited state file without restarting the
// gateway: the file becomes the authoritative ban list.
func (a *Server) handleWarpReload(w http.ResponseWriter, r *http.Request) {
	pool := a.manager.WarpPool()
	if pool == nil {
		writeAdminError(w, http.StatusConflict, "warp_disabled", "WARP is not enabled in the running configuration")
		return
	}
	added, removed, err := pool.ReloadBlacklist()
	if err != nil {
		writeAdminError(w, http.StatusBadRequest, "reload_failed", a.manager.Redact(err.Error()))
		return
	}
	w.Header().Set("Cache-Control", "no-store")
	httpx.WriteJSON(w, http.StatusOK, map[string]any{
		"added":      added,
		"removed":    removed,
		"count":      len(pool.Blacklist()),
		"state_path": pool.StatePath(),
	})
}

// errorsIsInvalidBlock keeps the handler from mapping every pool failure to a
// subnet problem while still giving the operator a specific code for a typo.
func errorsIsInvalidBlock(err error) bool {
	return errors.Is(err, warp.ErrInvalidEgressBlock)
}

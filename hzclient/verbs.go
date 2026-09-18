package hzclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"time"
)

// The verbs are flat and named by family — DeployStatus, SetSlotState,
// BanAdd — rather than grouped behind sub-structs. There are a dozen of them;
// sub-structs would buy nothing and cost every caller a hop, and flat names
// grep.

// SlotName addresses a slot by ROLE, which is the only way a caller should
// address one. The underlying letters a and b are hz's bookkeeping and they
// swap; current and next are stable.
type SlotName string

const (
	SlotCurrent SlotName = "current"
	SlotNext    SlotName = "next"
)

// SlotState is what to do to a slot. These are the actions hz names, not
// HAProxy's internal words: "down" becomes maint on the socket, which is why
// an observed state may read "maint" after asking for "down".
type SlotState string

const (
	StateUp    SlotState = "up"
	StateDrain SlotState = "drain"
	StateDown  SlotState = "down"
)

// DeployStatus fetches the deployment's configuration and both slots' live
// state.
func (c *Client) DeployStatus(ctx context.Context) (*DeployStatus, error) {
	var out DeployStatus
	if err := c.do(ctx, http.MethodGet, "/api/deploy/status", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SetSlotState brings a slot up, drains it, or takes it down.
//
// It returns as soon as hz has told HAProxy. It does NOT wait for the slot to
// reach that state: bringing a slot up is a request for health checks to start
// passing, and two consecutive probes take about six seconds. Use
// WaitForState, or the rolling verbs, when the wait is the point.
//
// The two arguments are distinct types so they cannot be transposed.
func (c *Client) SetSlotState(ctx context.Context, slot SlotName, state SlotState) (*DeployStateChangeResponse, error) {
	switch slot {
	case SlotCurrent, SlotNext:
	default:
		return nil, fmt.Errorf("hzclient: %q is not a slot; want %s or %s", slot, SlotCurrent, SlotNext)
	}
	switch state {
	case StateUp, StateDrain, StateDown:
	default:
		return nil, fmt.Errorf("hzclient: %q is not a slot state; want %s, %s or %s", state, StateUp, StateDrain, StateDown)
	}
	var out DeployStateChangeResponse
	if err := c.do(ctx, http.MethodPost, "/api/deploy/"+string(slot)+"/"+string(state), nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// DeploySwap exchanges the current and next labels and reloads HAProxy. It
// moves no traffic by itself: it renames which slot the next deploy cycle will
// treat as standby.
func (c *Client) DeploySwap(ctx context.Context) (*DeploySwapResponse, error) {
	var out DeploySwapResponse
	if err := c.do(ctx, http.MethodPost, "/api/deploy/swap", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MaintPageSet installs a custom 503 page and returns its md5, which is how a
// caller confirms hz stored the bytes it sent.
//
// hz refuses an empty page with a 400: clearing is a separate verb, so an empty
// string here is a caller that meant MaintPageClear and a truncated read that
// silently wiped the page would be worse than a refusal.
func (c *Client) MaintPageSet(ctx context.Context, html string) (*MaintPageResponse, error) {
	if html == "" {
		return nil, errors.New("hzclient: an empty maintenance page is not a page; use MaintPageClear")
	}
	var out MaintPageResponse
	body := struct {
		HTML string `json:"html"`
	}{HTML: html}
	if err := c.do(ctx, http.MethodPost, "/api/deploy/maint-page/set", body, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// MaintPageClear removes the custom 503 page, reverting to hz's default. The
// response carries no md5, because there is no page.
func (c *Client) MaintPageClear(ctx context.Context) (*MaintPageResponse, error) {
	var out MaintPageResponse
	if err := c.do(ctx, http.MethodPost, "/api/deploy/maint-page/clear", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// BanAdd blocks an address with an iptables DROP rule that hz also persists.
//
// A zero timeout is PERMANENT, which is hz's rule and not one this package can
// change. Because of that, a duration under a second is refused rather than
// truncated to zero: `500*time.Millisecond` silently becoming a permanent ban
// is exactly the class of bug a typed client is for.
//
// Banning is idempotent — hz answers an already-banned address with a success —
// and it refuses to ban hz's own addresses, which arrives as a 500 naming
// self-lockout.
func (c *Client) BanAdd(ctx context.Context, ip string, timeout time.Duration, reason string) error {
	if ip == "" {
		return errors.New("hzclient: no IP to ban")
	}
	if timeout < 0 {
		return fmt.Errorf("hzclient: ban timeout %s is negative; use 0 for a permanent ban", timeout)
	}
	if timeout > 0 && timeout < time.Second {
		return fmt.Errorf("hzclient: ban timeout %s is under a second, and hz counts in whole seconds, "+
			"so this would become 0 — which means PERMANENT. Use 0 deliberately, or at least 1s", timeout)
	}
	return c.okCall(ctx, "/api/ban/ban", BanRequest{
		IP:      ip,
		Timeout: int(timeout / time.Second),
		Reason:  reason,
	})
}

// BanRemove lifts a ban. hz ignores an address that is not banned, so this is
// idempotent too.
func (c *Client) BanRemove(ctx context.Context, ip string) error {
	if ip == "" {
		return errors.New("hzclient: no IP to unban")
	}
	return c.okCall(ctx, "/api/ban/unban", UnbanRequest{IP: ip})
}

// okCall posts a body and insists hz said ok, rather than treating a 200 as the
// whole answer.
func (c *Client) okCall(ctx context.Context, path string, in any) error {
	var out OKResponse
	if err := c.do(ctx, http.MethodPost, path, in, &out); err != nil {
		return err
	}
	if !out.OK {
		return fmt.Errorf("%s: hz answered 200 with ok=false", path)
	}
	return nil
}

// BanList returns every active ban.
//
// The timestamps are Unix seconds and they ARRIVE — see BanEntry, and the test
// that decodes a server-shaped payload to prove it. The script this replaces
// printed "created=- expires=never" for every ban it ever listed.
func (c *Client) BanList(ctx context.Context) ([]BanEntry, error) {
	var out BanListResponse
	if err := c.do(ctx, http.MethodGet, "/api/ban/list", nil, &out); err != nil {
		return nil, err
	}
	return out.Bans, nil
}

// SiteRollback reverts a static-folder service to the previous release by
// atomic symlink swap, and names the release now live.
//
// hz answers 400 when there is no previous release to go back to, which is the
// ordinary state of a site that has been deployed exactly once.
func (c *Client) SiteRollback(ctx context.Context) (*SiteRollbackResponse, error) {
	var out SiteRollbackResponse
	if err := c.do(ctx, http.MethodPost, "/api/site/rollback", nil, &out); err != nil {
		return nil, err
	}
	return &out, nil
}

// SiteReleases lists the retained releases, newest first, with the live one
// marked Current.
func (c *Client) SiteReleases(ctx context.Context) ([]SiteRelease, error) {
	var out []SiteRelease
	if err := c.do(ctx, http.MethodGet, "/api/site/releases", nil, &out); err != nil {
		return nil, err
	}
	return out, nil
}

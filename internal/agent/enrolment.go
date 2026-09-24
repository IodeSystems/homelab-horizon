package agent

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/cookiejar"
	"strings"
	"time"
)

// ENROLMENT: hz is the issuer now (plan/architecture.md, phase 4 item 13).
//
// WHAT CHANGED AND WHAT DID NOT. `hz-agent enroll` used to mint a secret
// locally and write its own record into hz's store, which only worked because
// hz and the agent were the same root on one box. A remote agent must not be
// able to write itself in. So the ISSUER moves to hz and NOTHING ELSE DOES:
// the store format (CredentialStore, a JSON list keyed by machine, 0600), the
// header (Authorize / PresentedSecret, four lines apart in credential.go), the
// hashing (SHA-256, hz holds the hash and never the secret), hz's verification
// (Server.agentCaller) and the poll (HTTPSource) are untouched, because none of
// them know where a credential came from.
//
// WHY BOTH HALVES LIVE IN THIS PACKAGE, again. The bug this whole area exists
// to prevent was a client and a server written in different packages against
// different assumptions, never exercised together (plan/privilege-audit.md
// §1.1). So the request the agent sends and the shape hz answers with are
// declared here, once, and internal/server marshals into them rather than
// re-declaring them. The end-to-end test in internal/server drives THIS client
// against the real routing table.
//
// WHAT AUTHORISES AN ENROLMENT: an hz ADMIN credential, supplied by the
// operator at the box, at enrolment time only. It is never written to disk on
// the machine being enrolled, never reaches the unit, and the running agent
// never holds it — the daemon holds the per-machine secret hz issues and
// nothing else. That is the "presence" the model already asks for
// (architecture.md, "Presence — a trip to the box to join a second segment"):
// enrolling a box is an act of authority, performed once, by somebody who has
// it.
//
// WHY NOT A BEARER BRANCH FOR THE SHARED ADMIN TOKEN. Same reason isAdmin did
// not grow one (agent_credential.go): it would make the shared token an API key
// on every admin surface. So this client does what `hz` itself does — exchanges
// the shared token for a session at /api/v1/auth/login — and additionally sends
// it as a Bearer credential, which authenticates only if it is a PERSONAL API
// token. One request path covers both credential shapes and widens nothing.

// EnrollPath is where hz issues a machine's agent credential.
const EnrollPath = "/api/v1/agent/enroll"

// loginPath is hz's admin-token-for-session exchange, the same one the hz CLI
// uses. Named here rather than imported so this package keeps no dependency on
// the CLI's wire types.
const loginPath = "/api/v1/auth/login"

// EnrollRequest is what the agent asks hz for.
type EnrollRequest struct {
	// Machine is who is enrolling. hz refuses a machine it does not declare —
	// that refusal is the whole point of the Machine record being the issuer.
	Machine string `json:"machine"`

	// CurrentHash is the SHA-256 of the credential this box already holds, or
	// "". The HASH and never the secret: re-enrolment has to be able to ask
	// "is what I hold still valid" (install runs enrolment every time), and
	// sending the secret to find out would put a working credential on the
	// wire for a question that a hash answers exactly as well. hz stores the
	// hash, so the comparison is the one it already makes.
	CurrentHash string `json:"currentHash,omitempty"`

	// Rotate mints a new secret even when CurrentHash still matches, retiring
	// the old one in the same write.
	Rotate bool `json:"rotate,omitempty"`

	// SegmentKeys is this box's WireGuard PUBLIC key for each segment it is a
	// member of. It rides on enrolment rather than on a channel of its own, and
	// that is the whole trust argument: enrolment is ALREADY the authenticated
	// act (an hz admin credential, supplied by the operator, at the box), so the
	// key arrives with exactly the authority the credential hz issues in the
	// same request arrives with. A separate "report my key" endpoint would be a
	// SECOND trust model for the same fact, and configmgr's registration
	// ceremony exists precisely because inventing one of those is a decision.
	//
	// PER SEGMENT, never per machine. A machine in two segments has two
	// interfaces and two key pairs, and one machine-level key would hand an
	// attacker who took wg-code the tunnel to wg-redline — the isolation the
	// whole segment model exists for (architecture.md, "Segments").
	//
	// THE PUBLIC HALF ONLY. The private one is minted on the box, written 0600
	// beside the agent credential and never transmitted; see
	// segmentkey.go. There is no field here it could travel in, which is the
	// only guarantee available, because a private key and a public key are the
	// same 44 characters and nothing that inspects the bytes can tell.
	SegmentKeys []SegmentKey `json:"segmentKeys,omitempty"`

	// RotateKeys lets a reported key REPLACE a different one hz already holds.
	//
	// Off by default, because a box re-enrolling with a different key is either
	// a rotation or an impostor and hz cannot tell from the request. Accepting
	// it silently is a peer takeover: whoever gets their key recorded receives
	// that machine's traffic. So the default assumption is IMPOSTOR — hz keeps
	// what it has and reports a conflict — and a rotation is made to say so,
	// which an operator does with `hz-agent enroll --rotate-keys` while standing
	// at the box with an admin credential in hand.
	RotateKeys bool `json:"rotateKeys,omitempty"`
}

// SegmentKey is one (segment, public key) pair reported by the box.
//
// Keyed by SEGMENT rather than by interface name even though the key is
// per-interface, for one reason: the interface name is a property hz owns
// (Segment.Interface) and the box does not learn it at enrolment. The two are
// one-to-one — ValidateSegments refuses two segments on one interface — so
// naming the segment names the interface, and it names it in the vocabulary
// both ends already share.
type SegmentKey struct {
	Segment   string `json:"segment"`
	PublicKey string `json:"publicKey"`
}

// The outcomes of reporting one key. They are distinct because an operator has
// a different next step for each, and collapsing them into ok/failed would put
// "hz already had this" and "hz thinks you are an impostor" in one bucket.
const (
	// SegmentKeyRecorded: hz wrote it. The member had no key, or had this one's
	// predecessor and the request asked for a rotation.
	SegmentKeyRecorded = "recorded"
	// SegmentKeyUnchanged: hz already holds exactly this key. Nothing written.
	SegmentKeyUnchanged = "unchanged"
	// SegmentKeyConflict: hz holds a DIFFERENT key for this member and the
	// request did not ask to rotate. Nothing written, and this is the state
	// that must never be silent — it is a rotation somebody forgot to declare
	// or a box claiming another's peering.
	SegmentKeyConflict = "conflict"
	// SegmentKeyUnaddressed: the machine is in the segment and has no address
	// on it, so there is no member entry to attach a key to. Legal, and the
	// state `hz machine add --segment` leaves.
	SegmentKeyUnaddressed = "unaddressed"
	// SegmentKeyUnknown: hz does not declare that segment, or does not have
	// this machine in it. The box reported a key for something hz does not
	// model.
	SegmentKeyUnknown = "unknown"
	// SegmentKeyInvalid: the reported string is not a WireGuard key.
	SegmentKeyInvalid = "invalid"
)

// SegmentKeyResult is what hz did with one reported key, per segment.
type SegmentKeyResult struct {
	Segment string `json:"segment"`
	Status  string `json:"status"`
	// Detail is the sentence for a human, present when the status alone does
	// not say what to do about it.
	Detail string `json:"detail,omitempty"`
}

// EnrollResponse is hz's answer.
//
// Secret is set ONLY on a mint, and is the one moment the secret exists outside
// the agent's token file. AlreadyEnrolled is the other outcome: what the box
// holds is what hz has, nothing was written, and no secret crosses.
type EnrollResponse struct {
	Machine         string `json:"machine"`
	Secret          string `json:"secret,omitempty"`
	AlreadyEnrolled bool   `json:"alreadyEnrolled,omitempty"`
	// Segments is what hz declares this machine to be in, echoed so enrolment
	// can print the membership the operator just bought. Informational.
	Segments []string `json:"segments,omitempty"`
	// Note is the multi-homed machine's declared reason, echoed for the same
	// purpose: enrolling a bridge should say out loud that it is one.
	Note string `json:"note,omitempty"`

	// SegmentKeys is what hz did with each key the request reported, one entry
	// per reported segment. Absent when none were reported.
	//
	// A CONFLICT DOES NOT FAIL THE REQUEST, deliberately. `hz-agent install`
	// enrols every time and the credential half has to stay idempotent, so a
	// refused key must not cost a box its credential. The refusal is carried
	// here instead and the command turns it into a non-zero exit with the
	// segment named — visible, actionable, and not a 500 anybody has to
	// correlate with a log line.
	SegmentKeys []SegmentKeyResult `json:"segmentKeys,omitempty"`
}

// Conflicts is every reported key hz refused to overwrite. A helper rather
// than a loop at each call site because "did anything get refused" is the
// question every caller asks and the one an ignored slice quietly answers no to.
func (r *EnrollResponse) Conflicts() []SegmentKeyResult {
	var out []SegmentKeyResult
	for _, k := range r.SegmentKeys {
		if k.Status == SegmentKeyConflict {
			out = append(out, k)
		}
	}
	return out
}

// ErrMachineNotDeclared is hz refusing to issue a credential for a machine it
// has never been told about. A value rather than a string because the agent has
// something useful to say about it — declare it first — and a caller that had
// to match on prose would break the day the prose improved.
var ErrMachineNotDeclared = errors.New("hz does not declare this machine")

// Enroller asks hz for this machine's agent credential.
type Enroller struct {
	// BaseURL is hz's address.
	BaseURL string

	// AdminToken is the operator's hz credential, held for the length of this
	// call and never stored. Either shape works: the shared admin token (which
	// is exchanged for a session below) or a personal API token (which
	// authenticates as a Bearer).
	AdminToken string

	Client *http.Client
}

func (e *Enroller) httpClient() (*http.Client, error) {
	if e.Client != nil {
		return e.Client, nil
	}
	// A cookie jar, because the shared-admin-token path answers with a session
	// cookie and the enrol request has to carry it back.
	jar, err := cookiejar.New(nil)
	if err != nil {
		return nil, err
	}
	return &http.Client{Timeout: 20 * time.Second, Jar: jar}, nil
}

// Enroll asks hz to issue this machine's credential.
//
// Returns hz's answer. A response with AlreadyEnrolled set carries no secret
// and means nothing was written at either end.
func (e *Enroller) Enroll(ctx context.Context, req EnrollRequest) (*EnrollResponse, error) {
	if strings.TrimSpace(e.BaseURL) == "" {
		return nil, errors.New("no hz address to enrol with; pass --hz")
	}
	if strings.TrimSpace(req.Machine) == "" {
		return nil, errors.New("cannot enrol without a machine name")
	}
	if strings.TrimSpace(e.AdminToken) == "" {
		return nil, errors.New("enrolling needs an hz admin credential — hz issues the credential now, so somebody with authority has to ask for it")
	}
	client, err := e.httpClient()
	if err != nil {
		return nil, err
	}

	// The shared-admin-token exchange, best effort: it is the only way the
	// shared token authenticates (isAdmin deliberately has no Bearer path for
	// it), and it is simply refused for a personal API token, which the Bearer
	// header below covers instead.
	e.login(ctx, client)

	body, err := json.Marshal(req)
	if err != nil {
		return nil, err
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(e.BaseURL, "/")+EnrollPath, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+e.AdminToken)

	resp, err := client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("asking hz to enrol %s: %w", req.Machine, err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))

	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		return nil, fmt.Errorf("%w: %s", ErrMachineNotDeclared, strings.TrimSpace(string(raw)))
	case http.StatusUnauthorized, http.StatusForbidden:
		return nil, fmt.Errorf("hz refused the admin credential (%d) — enrolment is an admin act, and the credential given is not one", resp.StatusCode)
	default:
		return nil, fmt.Errorf("hz refused the enrolment (%d): %s", resp.StatusCode, strings.TrimSpace(string(raw)))
	}

	var out EnrollResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("reading hz's answer: %w", err)
	}
	if !out.AlreadyEnrolled && strings.TrimSpace(out.Secret) == "" {
		return nil, errors.New("hz answered with neither a credential nor an already-enrolled acknowledgement")
	}
	return &out, nil
}

// login exchanges the shared admin token for a session cookie, ignoring
// failure: a personal API token is refused here and authenticates as a Bearer
// instead, and a genuinely bad credential is reported by the enrol request
// itself rather than by a guess made here.
func (e *Enroller) login(ctx context.Context, client *http.Client) {
	body, err := json.Marshal(map[string]string{"token": e.AdminToken})
	if err != nil {
		return
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(e.BaseURL, "/")+loginPath, bytes.NewReader(body))
	if err != nil {
		return
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return
	}
	_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
	_ = resp.Body.Close()
}

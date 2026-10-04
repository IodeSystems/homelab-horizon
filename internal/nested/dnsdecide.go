package nested

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"sort"
	"strings"

	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// THE PURE HALF of the child's DNS request (CLAUDE.md #12): when to ask the
// parent again, decided from what was last sent and answered and an instant
// passed in. dns.go builds, posts and records.
//
// THE CHILD NEVER PUBLISHES A ZONE IT DOES NOT OWN. It asks; the parent holds
// the zone, the credential and the grant, and answers per record.
//
// NEVER BLOCKING (CLAUDE.md #5). Nothing in boot or sync waits on the answer:
// a parent that is down leaves the last answer in place, and the next round
// asks again.

// DNSRefreshSeconds is how often an unchanged set is re-sent when the last
// answer was clean: the parent's view (its grant, its publish) can change
// without the child's set changing.
const DNSRefreshSeconds int64 = 15 * 60

// DNSRetrySeconds is how soon a set is re-sent after a failure, or while a
// record is still pending on the parent.
const DNSRetrySeconds int64 = 60

// DNSFingerprint is the identity of a request: the same records and address
// in any order give the same fingerprint.
func DNSFingerprint(req apitypes.InstanceDNSReq) string {
	recs := make([]apitypes.InstanceDNSRecordReq, len(req.Records))
	for i, r := range req.Records {
		vals := append([]string(nil), r.Values...)
		sort.Strings(vals)
		recs[i] = apitypes.InstanceDNSRecordReq{Name: strings.ToLower(r.Name), Type: strings.ToUpper(r.Type), Values: vals, TTL: r.TTL}
	}
	sort.Slice(recs, func(i, j int) bool {
		return recs[i].Name+"|"+recs[i].Type < recs[j].Name+"|"+recs[j].Type
	})
	raw, _ := json.Marshal(apitypes.InstanceDNSReq{Records: recs, PublicIP: req.PublicIP})
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:])
}

// DNSDue decides whether to send the set now. A changed set is always due;
// otherwise a set is re-sent after DNSRetrySeconds when the last attempt
// failed or left a record pending, and after DNSRefreshSeconds when it was
// clean.
func DNSDue(fingerprint, lastFingerprint string, lastAttemptUnix, nowUnix int64, lastClean bool) bool {
	if fingerprint != lastFingerprint || lastAttemptUnix == 0 {
		return true
	}
	wait := DNSRefreshSeconds
	if !lastClean {
		wait = DNSRetrySeconds
	}
	return nowUnix-lastAttemptUnix >= wait
}

// DNSAnswerClean reports whether an answer needs no early retry: nothing
// pending. A refusal is the parent operator's to resolve and is not retried
// early — it is re-sent on the refresh.
func DNSAnswerClean(resp apitypes.InstanceDNSResp) bool {
	for _, r := range resp.Records {
		if r.Status == apitypes.InstanceDNSPending {
			return false
		}
	}
	return true
}

// DNSLine is the one line the child's sync output and UI give for a record.
func DNSLine(r apitypes.InstanceDNSRecordStatus) string {
	what := r.Type + " " + r.Name + " -> " + strings.Join(r.Values, ", ")
	switch r.Status {
	case apitypes.InstanceDNSPublished:
		line := what + ": published by parent"
		if r.Converted {
			line += " (" + r.Reason + ")"
		}
		return line
	case apitypes.InstanceDNSRefused:
		return what + ": refused: " + r.Reason
	default:
		line := what + ": pending"
		if r.Reason != "" {
			line += " — " + r.Reason
		}
		return line
	}
}

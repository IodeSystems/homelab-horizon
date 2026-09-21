package main

import (
	"crypto/ecdh"
	"strings"
	"testing"
	"time"

	"github.com/iodesystems/homelab-horizon/configmgr"
	"github.com/iodesystems/homelab-horizon/internal/apitypes"
)

// The tests that decide whether this feature is real.
//
// Every other test here proves a blob was produced. These prove one opens — and
// the ones that matter most are the NEGATIVE controls: a verify that always
// says yes is worse than no verify at all, because it converts "we never
// checked" into "we checked and it was fine". So each sabotage below breaks the
// wrap in a specific, different way and asserts verify says NO.
//
// A second invariant runs through all of them: no environment key and no
// private key may appear on the wire or in any output. cmStub.sentAnywhere and
// the output assertions below check that directly.

// recoveryFixture is a stub hz with n recovery recipients registered and a
// keystore. It returns the private halves, which in real life live only in a
// password manager and never on this machine.
func recoveryFixture(t *testing.T, names ...string) (*cmStub, *client, *configmgr.Keystore, map[string]*ecdh.PrivateKey) {
	t.Helper()
	ks := testKeystore(t)
	s := newCMStub()
	keys := map[string]*ecdh.PrivateKey{}
	for _, n := range names {
		keys[n] = s.addRecoveryRecipient(t, n)
	}
	return s, s.start(t), ks, keys
}

func TestKeyNewWrapsToEveryRecoveryRecipient(t *testing.T) {
	// The automatic half: custody without anyone remembering a step. A new
	// environment is covered because minting is the moment it happens.
	s, c, _, privs := recoveryFixture(t, "ops", "successor")

	var out string
	err := func() error {
		var e error
		out = captureStdout(t, func() { e = cmKeyNew(c, []string{"--label", "2026-01", "staging/redline/app"}) })
		return e
	}()
	if err != nil {
		t.Fatalf("key new: %v", err)
	}
	if !strings.Contains(out, "Wrapped to 2 recovery recipient(s)") {
		t.Fatalf("minting did not report the wraps:\n%s", out)
	}

	s.mu.Lock()
	wraps := append([]apitypes.CMRecoveryWrapReq(nil), s.wraps...)
	s.mu.Unlock()
	if len(wraps) != 2 {
		t.Fatalf("want one wrap per recipient, got %d", len(wraps))
	}

	addr := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	for _, w := range wraps {
		priv, ok := privs[w.Recipient]
		if !ok {
			t.Fatalf("a wrap went to %q, which is not a recipient", w.Recipient)
		}
		blob, err := configmgr.DecodeEnvelope(w.Wrapped)
		if err != nil {
			t.Fatalf("%s: %v", w.Recipient, err)
		}
		key, err := configmgr.UnwrapEnvKey(priv, addr, blob)
		if err != nil {
			t.Fatalf("%s's wrap does not open with %s's key: %v", w.Recipient, w.Recipient, err)
		}
		if key.ID().String() != w.KeyID {
			t.Fatalf("%s's wrap holds key %s, filed as %s", w.Recipient, key.ID(), w.KeyID)
		}
	}

	// The invariant the whole design rests on: what crossed the wire is an
	// envelope, never the key.
	ks, err := configmgr.DefaultKeystore()
	if err != nil {
		t.Fatal(err)
	}
	k, err := ks.KeyForID(keyAddr(addr), wraps[0].KeyID)
	if err != nil {
		t.Fatal(err)
	}
	if s.sentAnywhere(k.Text()) {
		t.Fatal("the environment key itself was sent to hz")
	}
	if strings.Contains(out, k.Text()) {
		t.Fatal("the environment key was printed")
	}
}

func TestKeyNewFailsLoudlyWhenCustodyCannotBeEstablished(t *testing.T) {
	// A key with no custody that reported success is the silent state this
	// feature exists to make impossible. The key is already on disk, so nothing
	// is lost — the error says so, and names the command that closes the gap.
	s, c, _, _ := recoveryFixture(t, "ops")
	s.mu.Lock()
	// A recipient whose public key cannot be parsed: hz served it, the client
	// cannot wrap to it.
	s.recovery.Recipients[0].PublicKey = "hzpub-not-a-key"
	s.mu.Unlock()

	var err error
	captureStdout(t, func() { err = cmKeyNew(c, []string{"--label", "2026-01", "staging/redline/app"}) })
	if err == nil {
		t.Fatal("minting reported success with no wrap written")
	}
	for _, want := range []string{"was minted", "ONE place", "hz cm recovery backfill"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("the error does not say %q:\n%v", want, err)
		}
	}
	// The key IS on disk: a failure to establish custody must not look like a
	// failure to mint, or an operator will mint again and end up with two.
	ks, kerr := configmgr.DefaultKeystore()
	if kerr != nil {
		t.Fatal(kerr)
	}
	held, kerr := ks.List(keyAddr(configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}))
	if kerr != nil || len(held) != 1 {
		t.Fatalf("the minted key is not in the keystore: %v %+v", kerr, held)
	}
}

func TestKeyNewWithNoRecipientsStillWorksAndSaysWhatIsMissing(t *testing.T) {
	// Opt-in. A config with no recovery recipients must behave exactly as it
	// did before this feature existed — and say, without failing, that the key
	// now exists in one place.
	_, c, _, _ := recoveryFixture(t)
	var err error
	out := captureStdout(t, func() { err = cmKeyNew(c, []string{"--label", "2026-01", "staging/redline/app"}) })
	if err != nil {
		t.Fatalf("minting must not require custody to be configured: %v", err)
	}
	if !strings.Contains(out, "exactly one place") || !strings.Contains(out, "hz cm recovery keygen") {
		t.Fatalf("the no-custody state is not stated:\n%s", out)
	}
}

// --- the round trip --------------------------------------------------------

// backfillAndVerify is the full ceremony a test drives: put a key in the
// keystore, back-fill it to every recipient, then verify with one private key.
func backfillAndVerify(t *testing.T, c *client, addr string, priv *ecdh.PrivateKey, args ...string) (string, error) {
	t.Helper()
	withStdin(t, configmgr.MarshalMachinePrivateKey(priv)+"\n")
	var err error
	out := captureStdout(t, func() { err = cmRecoveryVerify(c, append([]string{addr}, args...)) })
	return out, err
}

func TestRecoveryRoundTripAcrossTwoRecipients(t *testing.T) {
	// Create a key, wrap it to two recovery recipients, open it with each, and
	// confirm a third unrelated key cannot. That last clause is the one that
	// makes the first two mean anything.
	s, c, ks, privs := recoveryFixture(t, "ops", "successor")
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := putKey(t, ks, addr, "2026-01", time.Now().UTC())

	var err error
	captureStdout(t, func() { err = cmRecoveryBackfill(c, nil) })
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	s.mu.Lock()
	n := len(s.wraps)
	s.mu.Unlock()
	if n != 2 {
		t.Fatalf("backfill wrote %d wraps, want one per recipient", n)
	}

	for name, priv := range privs {
		out, err := backfillAndVerify(t, c, "prod/redline/app", priv)
		if err != nil {
			t.Fatalf("%s should be able to open it: %v\n%s", name, err, out)
		}
		if !strings.Contains(out, "YES") || !strings.Contains(out, key.ID().String()) {
			t.Fatalf("%s: verify did not report success for the key:\n%s", name, out)
		}
		if strings.Contains(out, key.Text()) || strings.Contains(out, configmgr.MarshalMachinePrivateKey(priv)) {
			t.Fatal("verify printed key material")
		}
	}

	// A third, unrelated key. No wrap is addressed to it, so the answer is NO —
	// not a crash, not a silent pass.
	stranger, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	out, err := backfillAndVerify(t, c, "prod/redline/app", stranger)
	if err == nil {
		t.Fatalf("an unrelated key verified:\n%s", out)
	}
	if !strings.Contains(out, "NO") {
		t.Fatalf("the refusal does not say NO:\n%s", out)
	}
}

// TestVerifyFailsOnASabotagedWrap is the positive control, and it is the single
// most important test in this change.
//
// A verify that always says yes is worse than no verify at all. So the wrap is
// broken three different ways, each defeating a different check, and each must
// be caught:
//
//  1. a flipped bit in the ciphertext — the AEAD tag must fail;
//  2. an honest wrap of the WRONG environment key to the right recipient — it
//     decrypts perfectly, so only recomputing the key id catches it;
//  3. an honest wrap of the right key made at a DIFFERENT address — the address
//     is authenticated additional data, so it must not pass for this one.
func TestVerifyFailsOnASabotagedWrap(t *testing.T) {
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}

	sabotage := map[string]func(t *testing.T, s *cmStub, priv *ecdh.PrivateKey, key configmgr.EnvKey, keyID string) string{
		"flipped bit in the ciphertext": func(t *testing.T, s *cmStub, priv *ecdh.PrivateKey, key configmgr.EnvKey, _ string) string {
			blob, err := configmgr.WrapEnvKey(priv.PublicKey(), addr, key)
			if err != nil {
				t.Fatal(err)
			}
			// The last byte is inside the AEAD tag.
			blob[len(blob)-1] ^= 0x01
			return configmgr.EncodeEnvelope(blob)
		},
		"a different key, honestly wrapped": func(t *testing.T, s *cmStub, priv *ecdh.PrivateKey, _ configmgr.EnvKey, _ string) string {
			other := configmgr.NewEnvKey()
			blob, err := configmgr.WrapEnvKey(priv.PublicKey(), addr, other)
			if err != nil {
				t.Fatal(err)
			}
			return configmgr.EncodeEnvelope(blob)
		},
		"the right key, wrapped at another address": func(t *testing.T, s *cmStub, priv *ecdh.PrivateKey, key configmgr.EnvKey, _ string) string {
			elsewhere := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
			blob, err := configmgr.WrapEnvKey(priv.PublicKey(), elsewhere, key)
			if err != nil {
				t.Fatal(err)
			}
			return configmgr.EncodeEnvelope(blob)
		},
	}

	for name, break_ := range sabotage {
		t.Run(name, func(t *testing.T) {
			s, c, ks, privs := recoveryFixture(t, "ops")
			key := putKey(t, ks, addr, "2026-01", time.Now().UTC())
			priv := privs["ops"]

			// Store the sabotaged blob directly, bypassing the CLI: the point
			// is a bad wrap sitting in hz, however it got there.
			s.mu.Lock()
			s.recovery.Wraps = []apitypes.CMRecoveryWrap{{
				Environment: addr.Environment, App: addr.App, Role: addr.Role,
				KeyID:       key.ID().String(),
				Recipient:   "ops",
				Fingerprint: configmgr.FingerprintOf(priv.PublicKey()).String(),
				Wrapped:     break_(t, s, priv, key, key.ID().String()),
			}}
			s.mu.Unlock()

			out, err := backfillAndVerify(t, c, "prod/redline/app", priv)
			if err == nil {
				t.Fatalf("VERIFY PASSED a sabotaged wrap (%s). A verify that always says yes is worse than none:\n%s", name, out)
			}
			if !strings.Contains(out, "NO") {
				t.Fatalf("the failure does not say NO:\n%s", out)
			}
			if strings.Contains(out, key.Text()) || strings.Contains(out, configmgr.MarshalMachinePrivateKey(priv)) {
				t.Fatal("a failing verify printed key material")
			}
		})
	}
}

func TestVerifyPassesOnlyTheUnsabotagedTwin(t *testing.T) {
	// The other half of the control: the same fixture, with an INTACT wrap,
	// must pass. Without this the sabotage tests would also pass against a
	// verify that always says no.
	s, c, ks, privs := recoveryFixture(t, "ops")
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := putKey(t, ks, addr, "2026-01", time.Now().UTC())
	priv := privs["ops"]
	blob, err := configmgr.WrapEnvKey(priv.PublicKey(), addr, key)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.recovery.Wraps = []apitypes.CMRecoveryWrap{{
		Environment: addr.Environment, App: addr.App, Role: addr.Role,
		KeyID: key.ID().String(), Recipient: "ops",
		Fingerprint: configmgr.FingerprintOf(priv.PublicKey()).String(),
		Wrapped:     configmgr.EncodeEnvelope(blob),
	}}
	s.mu.Unlock()

	out, err := backfillAndVerify(t, c, "prod/redline/app", priv)
	if err != nil {
		t.Fatalf("an intact wrap did not verify: %v\n%s", err, out)
	}
	if !strings.Contains(out, "YES") {
		t.Fatalf("no positive answer:\n%s", out)
	}
}

func TestVerifySendsNothingAndRefusesAKeyInArgv(t *testing.T) {
	s, c, ks, privs := recoveryFixture(t, "ops")
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	putKey(t, ks, addr, "2026-01", time.Now().UTC())
	captureStdout(t, func() { _ = cmRecoveryBackfill(c, nil) })

	priv := privs["ops"]
	text := configmgr.MarshalMachinePrivateKey(priv)
	if _, err := backfillAndVerify(t, c, "prod/redline/app", priv); err != nil {
		t.Fatalf("verify: %v", err)
	}
	// hz must never receive a private key. There is no field for one and verify
	// sends no body at all — it is a GET and then arithmetic.
	if s.sentAnywhere(text) {
		t.Fatal("the recovery private key was sent to hz")
	}
	// And there is no flag that would put it in argv, which /proc publishes.
	withStdin(t, text+"\n")
	var err error
	captureStdout(t, func() { err = cmRecoveryVerify(c, []string{"prod/redline/app", "--key", text}) })
	if err == nil || !strings.Contains(err.Error(), "flag provided but not defined") {
		t.Fatalf("a --key flag was accepted or failed for the wrong reason: %v", err)
	}
}

func TestVerifyRejectsSomethingThatIsNotAKeyWithoutEchoingIt(t *testing.T) {
	_, c, _, _ := recoveryFixture(t, "ops")
	withStdin(t, "hunter2-this-is-not-a-key\n")
	var err error
	out := captureStdout(t, func() { err = cmRecoveryVerify(c, []string{"prod/redline/app"}) })
	if err == nil {
		t.Fatal("garbage was accepted as a recovery key")
	}
	// The error names the format, not the bytes: an error message is the most
	// common place a pasted secret gets logged.
	if strings.Contains(err.Error(), "hunter2") || strings.Contains(out, "hunter2") {
		t.Fatalf("the rejection echoed what was typed: %v\n%s", err, out)
	}
}

func TestVerifyIsSilentAboutOtherAddresses(t *testing.T) {
	// Custody is per key. A wrap at staging must not make prod look covered.
	s, c, ks, privs := recoveryFixture(t, "ops")
	staging := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	putKey(t, ks, staging, "2026-01", time.Now().UTC())
	captureStdout(t, func() { _ = cmRecoveryBackfill(c, nil) })
	s.mu.Lock()
	wrapped := len(s.recovery.Wraps)
	s.mu.Unlock()
	if wrapped != 1 {
		t.Fatalf("setup: %d wraps", wrapped)
	}

	out, err := backfillAndVerify(t, c, "prod/redline/app", privs["ops"])
	if err == nil {
		t.Fatalf("prod verified off a staging wrap:\n%s", out)
	}
	if !strings.Contains(out, "no wrap for prod/redline/app") {
		t.Fatalf("the answer does not name what is missing:\n%s", out)
	}
}

// --- backfill --------------------------------------------------------------

func TestBackfillReachesOnlyTheKeysThisMachineHolds(t *testing.T) {
	// The reach question, answered honestly: the local keystore. hz cannot
	// supply an environment key (it has never held one) and a stored wrap only
	// opens with a recovery private key, which is not on this box — so an
	// address whose key lives on another laptop is reported, never guessed at.
	s, c, ks, privs := recoveryFixture(t, "ops")
	held := configmgr.EnvKeyAddr{Environment: "staging", App: "redline", Role: "app"}
	key := putKey(t, ks, held, "2026-01", time.Now().UTC())

	// An address hz knows about because a wrap exists, whose key this machine
	// does NOT hold.
	s.mu.Lock()
	s.recovery.Wraps = append(s.recovery.Wraps, apitypes.CMRecoveryWrap{
		Environment: "prod", App: "redline", Role: "app",
		KeyID: "0123456789abcdef", Recipient: "ops", Wrapped: "hzenv-elsewhere",
	})
	s.mu.Unlock()

	var err error
	out := captureStdout(t, func() { err = cmRecoveryBackfill(c, nil) })
	if err != nil {
		t.Fatalf("backfill: %v", err)
	}
	s.mu.Lock()
	sent := append([]apitypes.CMRecoveryWrapReq(nil), s.wraps...)
	s.mu.Unlock()
	if len(sent) != 1 || sent[0].Environment != "staging" || sent[0].KeyID != key.ID().String() {
		t.Fatalf("backfill wrapped something other than the key it holds: %+v", sent)
	}
	if !strings.Contains(out, "wrapped staging/redline/app") {
		t.Fatalf("backfill did not report what it did:\n%s", out)
	}
	if strings.Contains(out, key.Text()) {
		t.Fatal("backfill printed key material")
	}

	// ls reports the gap it cannot close, and says it cannot close it.
	lsOut := captureStdout(t, func() {
		if err := cmRecoveryList(c, nil); err != nil {
			t.Fatalf("ls: %v", err)
		}
	})
	if !strings.Contains(lsOut, "prod/redline/app") {
		t.Fatalf("ls hides an address hz holds a wrap for:\n%s", lsOut)
	}
	_ = privs
}

func TestBackfillIsIdempotentAndDryRunSendsNothing(t *testing.T) {
	s, c, ks, _ := recoveryFixture(t, "ops", "successor")
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	putKey(t, ks, addr, "2026-01", time.Now().UTC())

	dry := captureStdout(t, func() {
		if err := cmRecoveryBackfill(c, []string{"--dry-run"}); err != nil {
			t.Fatalf("dry run: %v", err)
		}
	})
	if !strings.Contains(dry, "would wrap") || !strings.Contains(dry, "Nothing was sent") {
		t.Fatalf("dry run output:\n%s", dry)
	}
	s.mu.Lock()
	n := len(s.wraps)
	s.mu.Unlock()
	if n != 0 {
		t.Fatalf("a dry run sent %d wraps", n)
	}

	captureStdout(t, func() {
		if err := cmRecoveryBackfill(c, nil); err != nil {
			t.Fatalf("backfill: %v", err)
		}
	})
	second := captureStdout(t, func() {
		if err := cmRecoveryBackfill(c, nil); err != nil {
			t.Fatalf("second backfill: %v", err)
		}
	})
	if !strings.Contains(second, "0 wrap(s) written, 2 already stored") {
		t.Fatalf("a repeat backfill was not a no-op:\n%s", second)
	}
}

func TestBackfillWithNoRecipientsOrNoKeysIsANoOp(t *testing.T) {
	t.Run("no recipients", func(t *testing.T) {
		_, c, ks, _ := recoveryFixture(t)
		putKey(t, ks, configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}, "2026-01", time.Now().UTC())
		out := captureStdout(t, func() {
			if err := cmRecoveryBackfill(c, nil); err != nil {
				t.Fatalf("backfill: %v", err)
			}
		})
		if !strings.Contains(out, "No recovery recipients") {
			t.Fatalf("output:\n%s", out)
		}
	})
	t.Run("no keys held", func(t *testing.T) {
		_, c, _, _ := recoveryFixture(t, "ops")
		out := captureStdout(t, func() {
			if err := cmRecoveryBackfill(c, nil); err != nil {
				t.Fatalf("backfill: %v", err)
			}
		})
		if !strings.Contains(out, "holds no environment keys") {
			t.Fatalf("output:\n%s", out)
		}
	})
}

// --- ls --------------------------------------------------------------------

func TestRecoveryListSurfacesTheGap(t *testing.T) {
	// The gap this exists to make visible: a key wrapped to one recipient and
	// not another. It is silent everywhere else and is discovered during a
	// recovery that then half-fails.
	s, c, ks, privs := recoveryFixture(t, "ops", "successor")
	addr := configmgr.EnvKeyAddr{Environment: "prod", App: "redline", Role: "app"}
	key := putKey(t, ks, addr, "2026-01", time.Now().UTC())
	blob, err := configmgr.WrapEnvKey(privs["ops"].PublicKey(), addr, key)
	if err != nil {
		t.Fatal(err)
	}
	s.mu.Lock()
	s.recovery.Wraps = []apitypes.CMRecoveryWrap{{
		Environment: addr.Environment, App: addr.App, Role: addr.Role,
		KeyID: key.ID().String(), Recipient: "ops", Wrapped: configmgr.EncodeEnvelope(blob),
	}}
	s.mu.Unlock()

	out := captureStdout(t, func() {
		if err := cmRecoveryList(c, nil); err != nil {
			t.Fatalf("ls: %v", err)
		}
	})
	if !strings.Contains(out, "MISSING: successor") {
		t.Fatalf("ls did not name the recipient with no wrap:\n%s", out)
	}
	if !strings.Contains(out, "hz cm recovery backfill") {
		t.Fatalf("ls did not say how to close it:\n%s", out)
	}
	if strings.Contains(out, key.Text()) {
		t.Fatal("ls printed key material")
	}
}

func TestRecoveryListWithNothingConfiguredExplainsTheRisk(t *testing.T) {
	_, c, _, _ := recoveryFixture(t)
	out := captureStdout(t, func() {
		if err := cmRecoveryList(c, nil); err != nil {
			t.Fatalf("ls: %v", err)
		}
	})
	for _, want := range []string{"No recovery recipients", "destroys the secrets rather than locking them", "hz cm recovery keygen"} {
		if !strings.Contains(out, want) {
			t.Fatalf("the empty state does not say %q:\n%s", want, out)
		}
	}
}

func TestRecoveryListNotesAWrapToADelistedRecipient(t *testing.T) {
	// Removal is not revocation, and a listing that quietly dropped the wrap
	// would say the opposite of the truth.
	s, c, _, _ := recoveryFixture(t, "ops")
	s.mu.Lock()
	s.recovery.Wraps = []apitypes.CMRecoveryWrap{{
		Environment: "prod", App: "redline", Role: "app",
		KeyID: "0123456789abcdef", Recipient: "departed", Wrapped: "hzenv-blob",
	}}
	s.mu.Unlock()
	out := captureStdout(t, func() {
		if err := cmRecoveryList(c, nil); err != nil {
			t.Fatalf("ls: %v", err)
		}
	})
	if !strings.Contains(out, "no longer a listed recipient") {
		t.Fatalf("ls hid a past grant that is still openable:\n%s", out)
	}
}

// --- add / rm --------------------------------------------------------------

func TestRecoveryAddSendsOnlyAPublicKey(t *testing.T) {
	s, c, _, _ := recoveryFixture(t)
	priv, err := configmgr.NewMachineKey()
	if err != nil {
		t.Fatal(err)
	}
	pub := configmgr.MarshalMachinePublicKey(priv.PublicKey())

	out := captureStdout(t, func() {
		if err := cmRecoveryAdd(c, []string{"ops", "--public-key", pub}); err != nil {
			t.Fatalf("add: %v", err)
		}
	})
	if !strings.Contains(out, configmgr.FingerprintOf(priv.PublicKey()).String()) {
		t.Fatalf("add did not print the fingerprint it derived:\n%s", out)
	}
	if !strings.Contains(out, "hz cm recovery backfill") {
		t.Fatalf("add did not say that existing keys are NOT covered:\n%s", out)
	}
	if s.sentAnywhere(configmgr.MarshalMachinePrivateKey(priv)) {
		t.Fatal("a private key was sent to hz")
	}
	s.mu.Lock()
	n := len(s.recovery.Recipients)
	s.mu.Unlock()
	if n != 1 {
		t.Fatalf("want one recipient, got %d", n)
	}
}

func TestRecoveryAddRefusesSomethingThatIsNotAPublicKey(t *testing.T) {
	s, c, _, _ := recoveryFixture(t)
	if err := cmRecoveryAdd(c, []string{"ops", "--public-key", "hzpub-nope"}); err == nil {
		t.Fatal("a malformed public key was accepted")
	}
	if len(s.rawBodies) != 0 {
		t.Fatalf("a malformed key was sent to hz anyway: %v", s.rawBodies)
	}
}

func TestRecoveryRemoveRequiresTypingTheNameAndSaysItIsNotRevocation(t *testing.T) {
	_, c, _, _ := recoveryFixture(t, "ops")
	withStdin(t, "something-else\n")
	var err error
	out := captureStdout(t, func() { err = cmRecoveryRemove(c, []string{"ops"}) })
	if err == nil {
		t.Fatal("removal proceeded without the name being typed")
	}
	if !strings.Contains(out, "NOT a revocation") {
		t.Fatalf("the prompt does not say what removal does not do:\n%s", out)
	}
}

func TestRecoveryKeygenRefusesANonTerminal(t *testing.T) {
	// stdout in a test is a pipe, which is exactly the destination this must
	// refuse: a redirect is how the one key that opens everything ends up in a
	// file with the wrong mode or a CI log.
	var err error
	out := captureStdout(t, func() { err = cmRecoveryKeygen(nil) })
	if err == nil {
		t.Fatal("keygen printed a private key to a pipe")
	}
	if !strings.Contains(err.Error(), "not a terminal") {
		t.Fatalf("wrong refusal: %v", err)
	}
	if strings.Contains(out, "hzpriv-") {
		t.Fatal("keygen emitted key material before refusing")
	}
}

func TestRecoverySubcommandDispatch(t *testing.T) {
	_, c, _, _ := recoveryFixture(t)
	if err := runCMRecovery(c, nil); err == nil || !strings.Contains(err.Error(), "subcommand required") {
		t.Fatalf("no subcommand: %v", err)
	}
	if err := runCMRecovery(c, []string{"nope"}); err == nil || !strings.Contains(err.Error(), "unknown") {
		t.Fatalf("unknown subcommand: %v", err)
	}
}

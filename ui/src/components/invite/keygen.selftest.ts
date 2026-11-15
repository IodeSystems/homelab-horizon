/**
 * Assertions for the invite page's key generation (keygen.ts).
 *
 * No test framework — a plain Node program, the shape the other *.selftest.ts
 * files established. `pnpm test` runs it. Node's WebCrypto has X25519, so this
 * is the same code path a browser runs.
 *
 * What it is defending:
 *   The keypair is a real X25519 pair: the public key is the one the private
 *   key derives (checked against an independent import, and against the RFC
 *   7748 §6.1 vector), and two pairs agree on a shared secret.
 *   The request to hz carries the public key — the pair's own — and nothing
 *   else of the pair.
 *   The private key is put in the config exactly once, where hz left the
 *   placeholder.
 */
import {
  INVITE_PRIVATE_KEY_PLACEHOLDER,
  b64urlToB64,
  cleanDeviceName,
  fillConfig,
  generateWGKeyPair,
  redeemRequest,
  x25519Unavailable,
} from "./keygen.ts";

let failures = 0;
let checks = 0;
function check(ok: boolean, what: string): void {
  checks += 1;
  if (!ok) {
    failures += 1;
    console.log(`  FAIL  ${what}`);
  }
}

const subtle = globalThis.crypto.subtle;
const b64ToBytes = (s: string) => Uint8Array.from(atob(s), (c) => c.charCodeAt(0));
const hexToBytes = (h: string) => Uint8Array.from(h.match(/../g)!.map((x) => parseInt(x, 16)));
// PKCS#8 wrapper of a raw X25519 private key (RFC 8410).
const PKCS8_PREFIX = hexToBytes("302e020100300506032b656e04220420");

/** The public key a raw private key derives, by a route keygen.ts does not use. */
async function publicOf(rawPrivB64: string): Promise<string> {
  const der = new Uint8Array([...PKCS8_PREFIX, ...b64ToBytes(rawPrivB64)]);
  const key = await subtle.importKey("pkcs8", der, { name: "X25519" }, true, ["deriveBits"]);
  const jwk = await subtle.exportKey("jwk", key);
  return b64urlToB64(jwk.x!);
}

async function main(): Promise<void> {
  check((await x25519Unavailable()) === "", "Node's WebCrypto has X25519 (instrument: else nothing below means anything)");
  check((await x25519Unavailable(null)).includes("secure"), "no WebCrypto → a sentence, not a crash");
  const noX = { generateKey: () => Promise.reject(new Error("NotSupportedError")) } as unknown as SubtleCrypto;
  check((await x25519Unavailable(noX)).includes("X25519"), "no X25519 → a sentence naming it");

  // RFC 7748 §6.1, Alice: the derivation route used below is right.
  const alicePriv = btoa(String.fromCharCode(...hexToBytes("77076d0a7318a57d3c16c17251b26645df4c2f87ebc0992ab177fba51db92c2a")));
  const alicePub = btoa(String.fromCharCode(...hexToBytes("8520f0098930a754748b7ddcb43ef75a0dbf3a0d26381af4eba4a98eaa9b4e6a")));
  check((await publicOf(alicePriv)) === alicePub, "RFC 7748 vector: the independent derivation is correct");

  const a = await generateWGKeyPair();
  const b = await generateWGKeyPair();
  check(b64ToBytes(a.privateKey).length === 32 && b64ToBytes(a.publicKey).length === 32, "raw 32-byte keys");
  check(/^[A-Za-z0-9+/]{43}=$/.test(a.publicKey) && /^[A-Za-z0-9+/]{43}=$/.test(a.privateKey), "WireGuard base64 (44 chars, = padded)");
  check(a.privateKey !== b.privateKey && a.publicKey !== b.publicKey, "two pairs differ");
  check((await publicOf(a.privateKey)) === a.publicKey, "the public key is the one the private key derives");

  // Agreement: a.priv × b.pub == b.priv × a.pub, through the exported keys.
  const imp = async (priv: string) =>
    subtle.importKey("pkcs8", new Uint8Array([...PKCS8_PREFIX, ...b64ToBytes(priv)]), { name: "X25519" }, false, ["deriveBits"]);
  const pubKey = (pub: string) => subtle.importKey("raw", b64ToBytes(pub), { name: "X25519" }, false, []);
  const s1 = new Uint8Array(await subtle.deriveBits({ name: "X25519", public: await pubKey(b.publicKey) }, await imp(a.privateKey), 256));
  const s2 = new Uint8Array(await subtle.deriveBits({ name: "X25519", public: await pubKey(a.publicKey) }, await imp(b.privateKey), 256));
  check(s1.length === 32 && s1.every((x, i) => x === s2[i]), "the two pairs agree on a shared secret");

  // The request to hz.
  const req = redeemRequest("tok", a, "  carl phone ");
  check(req.publicKey === a.publicKey, "the submitted public key is the pair's own");
  check(JSON.stringify(Object.keys(req).sort()) === JSON.stringify(["name", "publicKey", "token"]), "the request has only token, publicKey, name");
  check(!JSON.stringify(req).includes(a.privateKey), "the private key is not in the request");
  check(req.name === "carl-phone", "device name cleaned");
  check(cleanDeviceName("a  b") === "a-b", "whitespace runs → one dash");

  // The config.
  const cfg = `[Interface]\nPrivateKey = ${INVITE_PRIVATE_KEY_PLACEHOLDER}\nAddress = 10.101.0.2/24\n`;
  const filled = fillConfig(cfg, a.privateKey);
  check(filled.includes(`PrivateKey = ${a.privateKey}\n`) && !filled.includes(INVITE_PRIVATE_KEY_PLACEHOLDER), "the key is put where the placeholder was");
  let threw = 0;
  for (const bad of ["[Interface]\nAddress = x\n", cfg + cfg]) {
    try {
      fillConfig(bad, a.privateKey);
    } catch {
      threw += 1;
    }
  }
  check(threw === 2, "zero or two placeholders are refused, not guessed at");

  console.log(`keygen.selftest: ${checks - failures}/${checks} passed`);
  if (failures > 0) process.exit(1);
}

main().catch((e) => {
  console.log("  FAIL  threw:", e);
  process.exit(1);
});
